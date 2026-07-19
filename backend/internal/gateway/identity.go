package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

const (
	GatewayAudience = "bosun-llm-gateway"
	podNameExtraKey = "authentication.kubernetes.io/pod-name"
	podUIDExtraKey  = "authentication.kubernetes.io/pod-uid"
)

var (
	ErrInvalidToken       = errors.New("invalid gateway token")
	ErrIdentityMismatch   = errors.New("gateway identity mismatch")
	ErrSessionUnavailable = errors.New("session is unavailable")
	ErrTokenReviewFailed  = errors.New("token review failed")
)

type TokenReviewClient interface {
	Create(context.Context, *authenticationv1.TokenReview, metav1.CreateOptions) (*authenticationv1.TokenReview, error)
}

type SessionIdentity struct {
	SessionID    string
	Namespace    string
	CRName       string
	Phase        string
	ProviderMode string
}

type SessionResolver interface {
	Resolve(context.Context, string, string) (SessionIdentity, error)
}

type Identity struct {
	SessionID    string
	Namespace    string
	PodName      string
	ProviderMode string
}

type Authenticator struct {
	reviews  TokenReviewClient
	sessions SessionResolver
	timeout  time.Duration
}

func NewAuthenticator(reviews TokenReviewClient, sessions SessionResolver, timeout time.Duration) *Authenticator {
	return &Authenticator{reviews: reviews, sessions: sessions, timeout: timeout}
}

func (a *Authenticator) Authenticate(ctx context.Context, token string) (Identity, error) {
	if strings.TrimSpace(token) == "" {
		return Identity{}, ErrInvalidToken
	}
	reviewCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	review, err := a.reviewWithRetry(reviewCtx, token)
	if err != nil {
		return Identity{}, err
	}
	if !review.Status.Authenticated || !contains(review.Status.Audiences, GatewayAudience) {
		return Identity{}, ErrInvalidToken
	}

	namespace, serviceAccount, ok := parseServiceAccountUsername(review.Status.User.Username)
	if !ok {
		return Identity{}, ErrIdentityMismatch
	}
	podName, ok := singleExtra(review.Status.User.Extra, podNameExtraKey)
	if !ok {
		return Identity{}, ErrIdentityMismatch
	}
	if _, ok := singleExtra(review.Status.User.Extra, podUIDExtraKey); !ok {
		return Identity{}, ErrIdentityMismatch
	}
	const serviceAccountPrefix = "bosun-session-"
	if !strings.HasPrefix(serviceAccount, serviceAccountPrefix) {
		return Identity{}, ErrIdentityMismatch
	}
	suffix := strings.TrimPrefix(serviceAccount, serviceAccountPrefix)
	if len(suffix) != 12 || podName != "agent-"+suffix {
		return Identity{}, ErrIdentityMismatch
	}

	session, err := a.sessions.Resolve(ctx, namespace, "sess-"+suffix)
	if err != nil {
		if errors.Is(err, ErrSessionUnavailable) {
			return Identity{}, ErrSessionUnavailable
		}
		return Identity{}, fmt.Errorf("resolve gateway session identity: %w", err)
	}
	if session.Namespace != namespace ||
		sessionidentity.CRName(session.SessionID) != session.CRName ||
		sessionidentity.ServiceAccountName(session.SessionID) != serviceAccount ||
		sessionidentity.PodName(session.SessionID) != podName {
		return Identity{}, ErrIdentityMismatch
	}
	if !phaseAllowsLLM(session.Phase) {
		return Identity{}, ErrSessionUnavailable
	}
	return Identity{
		SessionID:    session.SessionID,
		Namespace:    namespace,
		PodName:      podName,
		ProviderMode: session.ProviderMode,
	}, nil
}

func (a *Authenticator) reviewWithRetry(ctx context.Context, token string) (*authenticationv1.TokenReview, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		review, err := a.reviews.Create(ctx, &authenticationv1.TokenReview{
			Spec: authenticationv1.TokenReviewSpec{
				Token:     token,
				Audiences: []string{GatewayAudience},
			},
		}, metav1.CreateOptions{})
		if err == nil {
			return review, nil
		}
		lastErr = err
		if attempt == 0 {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("%w: %v", ErrTokenReviewFailed, ctx.Err())
			case <-timer.C:
			}
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrTokenReviewFailed, lastErr)
}

func parseServiceAccountUsername(username string) (string, string, bool) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(username, prefix), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func singleExtra(extra map[string]authenticationv1.ExtraValue, key string) (string, bool) {
	values := extra[key]
	returnValue := ""
	if len(values) == 1 {
		returnValue = values[0]
	}
	return returnValue, len(values) == 1 && returnValue != ""
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func phaseAllowsLLM(phase string) bool {
	switch phase {
	case "Provisioning", "Running", "Idle", "Restoring":
		return true
	default:
		return false
	}
}

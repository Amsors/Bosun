package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

const testSessionID = "018f9c6e-1234-7000-8000-abcdef012401"

type fakeTokenReviewer struct {
	response *authenticationv1.TokenReview
	err      error
	calls    int
	request  *authenticationv1.TokenReview
}

func (f *fakeTokenReviewer) Create(
	_ context.Context,
	request *authenticationv1.TokenReview,
	_ metav1.CreateOptions,
) (*authenticationv1.TokenReview, error) {
	f.calls++
	f.request = request
	if f.err != nil {
		return nil, f.err
	}
	return f.response.DeepCopy(), nil
}

type fakeSessionResolver struct {
	identity SessionIdentity
	err      error
}

func (f fakeSessionResolver) Resolve(context.Context, string, string) (SessionIdentity, error) {
	return f.identity, f.err
}

func TestAuthenticatorValidatesBoundSessionIdentity(t *testing.T) {
	namespace := "bosun-u-123456789abc"
	reviewer := &fakeTokenReviewer{response: validReview(namespace, testSessionID)}
	resolver := fakeSessionResolver{identity: validSessionIdentity(namespace, testSessionID)}
	identity, err := NewAuthenticator(reviewer, resolver, time.Second).Authenticate(context.Background(), "projected-token")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if identity.SessionID != testSessionID || identity.Namespace != namespace {
		t.Fatalf("identity = %#v", identity)
	}
	if reviewer.request.Spec.Token != "projected-token" ||
		len(reviewer.request.Spec.Audiences) != 1 ||
		reviewer.request.Spec.Audiences[0] != GatewayAudience {
		t.Fatalf("TokenReview spec = %#v", reviewer.request.Spec)
	}
}

func TestAuthenticatorRejectsInvalidAndForgedIdentities(t *testing.T) {
	namespace := "bosun-u-123456789abc"
	tests := []struct {
		name    string
		review  *authenticationv1.TokenReview
		session SessionIdentity
		want    error
	}{
		{
			name: "expired token",
			review: &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
				Authenticated: false,
			}},
			session: validSessionIdentity(namespace, testSessionID),
			want:    ErrInvalidToken,
		},
		{
			name: "audience mismatch",
			review: func() *authenticationv1.TokenReview {
				review := validReview(namespace, testSessionID)
				review.Status.Audiences = []string{"another-audience"}
				return review
			}(),
			session: validSessionIdentity(namespace, testSessionID),
			want:    ErrInvalidToken,
		},
		{
			name: "forged pod",
			review: func() *authenticationv1.TokenReview {
				review := validReview(namespace, testSessionID)
				review.Status.User.Extra[podNameExtraKey] = authenticationv1.ExtraValue{"agent-forged"}
				return review
			}(),
			session: validSessionIdentity(namespace, testSessionID),
			want:    ErrIdentityMismatch,
		},
		{
			name:    "session A token cannot resolve as session B",
			review:  validReview(namespace, testSessionID),
			session: validSessionIdentity(namespace, "018f9c6e-1234-7000-8000-abcdef012499"),
			want:    ErrIdentityMismatch,
		},
		{
			name:   "hibernated session",
			review: validReview(namespace, testSessionID),
			session: func() SessionIdentity {
				identity := validSessionIdentity(namespace, testSessionID)
				identity.Phase = "Hibernated"
				return identity
			}(),
			want: ErrSessionUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := NewAuthenticator(
				&fakeTokenReviewer{response: tt.review},
				fakeSessionResolver{identity: tt.session},
				time.Second,
			)
			_, err := authenticator.Authenticate(context.Background(), "token")
			if !errors.Is(err, tt.want) {
				t.Fatalf("Authenticate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestAuthenticatorRetriesTokenReviewFailureAndFailsClosed(t *testing.T) {
	reviewer := &fakeTokenReviewer{err: errors.New("apiserver unavailable")}
	authenticator := NewAuthenticator(reviewer, fakeSessionResolver{}, time.Second)
	_, err := authenticator.Authenticate(context.Background(), "token")
	if !errors.Is(err, ErrTokenReviewFailed) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if reviewer.calls != 2 {
		t.Fatalf("TokenReview calls = %d, want 2", reviewer.calls)
	}
}

func validReview(namespace, sessionID string) *authenticationv1.TokenReview {
	return &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
		Authenticated: true,
		Audiences:     []string{GatewayAudience},
		User: authenticationv1.UserInfo{
			Username: "system:serviceaccount:" + namespace + ":" + sessionidentity.ServiceAccountName(sessionID),
			Extra: map[string]authenticationv1.ExtraValue{
				podNameExtraKey: {sessionidentity.PodName(sessionID)},
				podUIDExtraKey:  {"pod-uid"},
			},
		},
	}}
}

func validSessionIdentity(namespace, sessionID string) SessionIdentity {
	return SessionIdentity{
		SessionID:    sessionID,
		Namespace:    namespace,
		CRName:       sessionidentity.CRName(sessionID),
		Phase:        "Running",
		ProviderMode: "platform",
	}
}

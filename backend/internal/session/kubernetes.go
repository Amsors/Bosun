package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

type Action string

const (
	ActionHibernate   Action = "hibernate"
	ActionResume      Action = "resume"
	ActionRetry       Action = "retry"
	k8sRequestTimeout        = 5 * time.Second
)

type RuntimeControl interface {
	Ensure(context.Context, Session) error
	Get(context.Context, Session) (*bosunv1alpha1.AgentSession, error)
	Mutate(context.Context, Session, Action, uuid.UUID, time.Time) (*bosunv1alpha1.AgentSession, error)
	Delete(context.Context, Session) error
}

type CRControl struct {
	client client.Client
}

func NewCRControl(k8sClient client.Client) *CRControl {
	return &CRControl{client: k8sClient}
}

func (c *CRControl) Ensure(ctx context.Context, session Session) error {
	ctx, cancel := context.WithTimeout(ctx, k8sRequestTimeout)
	defer cancel()
	cr := agentSessionFromDomain(session)
	if err := c.client.Create(ctx, cr); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create AgentSession %s/%s: %w", cr.Namespace, cr.Name, err)
		}
		var existing bosunv1alpha1.AgentSession
		if err := c.client.Get(ctx, client.ObjectKeyFromObject(cr), &existing); err != nil {
			return fmt.Errorf("get existing AgentSession %s/%s: %w", cr.Namespace, cr.Name, err)
		}
		if existing.Spec.SessionID != session.ID.String() || existing.Spec.UserID != session.UserID.String() {
			return fmt.Errorf("AgentSession %s/%s belongs to another session", cr.Namespace, cr.Name)
		}
	}
	return nil
}

func (c *CRControl) Get(ctx context.Context, session Session) (*bosunv1alpha1.AgentSession, error) {
	ctx, cancel := context.WithTimeout(ctx, k8sRequestTimeout)
	defer cancel()
	var cr bosunv1alpha1.AgentSession
	err := c.client.Get(ctx, types.NamespacedName{Namespace: session.CRNamespace, Name: session.CRName}, &cr)
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get AgentSession %s/%s: %w", session.CRNamespace, session.CRName, err)
	}
	if cr.Spec.SessionID != session.ID.String() || cr.Spec.UserID != session.UserID.String() {
		return nil, ErrNotFound
	}
	return &cr, nil
}

func (c *CRControl) Mutate(
	ctx context.Context,
	session Session,
	action Action,
	nonce uuid.UUID,
	now time.Time,
) (*bosunv1alpha1.AgentSession, error) {
	ctx, cancel := context.WithTimeout(ctx, k8sRequestTimeout)
	defer cancel()
	key := types.NamespacedName{Namespace: session.CRNamespace, Name: session.CRName}
	var updated bosunv1alpha1.AgentSession
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var current bosunv1alpha1.AgentSession
		if err := c.client.Get(ctx, key, &current); err != nil {
			if apierrors.IsNotFound(err) {
				return ErrNotFound
			}
			return fmt.Errorf("get AgentSession before %s: %w", action, err)
		}
		if current.Spec.SessionID != session.ID.String() || current.Spec.UserID != session.UserID.String() {
			return ErrNotFound
		}
		if !allowedTransition(current.Status.Phase, action) || transitionAlreadyRequested(&current, action) {
			return ErrInvalidTransition
		}
		switch action {
		case ActionHibernate:
			current.Spec.DesiredState = bosunv1alpha1.DesiredStateHibernated
		case ActionResume, ActionRetry:
			current.Spec.DesiredState = bosunv1alpha1.DesiredStateRunning
			current.Spec.ResumeNonce = nonce.String()
			if current.Annotations == nil {
				current.Annotations = make(map[string]string, 1)
			}
			current.Annotations[sessionidentity.LastActiveAnnotation] = now.UTC().Format(time.RFC3339)
		default:
			return ErrInvalidTransition
		}
		if err := c.client.Update(ctx, &current); err != nil {
			return fmt.Errorf("update AgentSession for %s: %w", action, err)
		}
		updated = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (c *CRControl) Delete(ctx context.Context, session Session) error {
	ctx, cancel := context.WithTimeout(ctx, k8sRequestTimeout)
	defer cancel()
	cr, err := c.Get(ctx, session)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	uid := cr.UID
	rv := cr.ResourceVersion
	propagation := metav1.DeletePropagationBackground
	err = c.client.Delete(ctx, cr, &client.DeleteOptions{Raw: &metav1.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &uid, ResourceVersion: &rv},
		PropagationPolicy: &propagation,
	}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete AgentSession %s/%s: %w", cr.Namespace, cr.Name, err)
	}
	return nil
}

func agentSessionFromDomain(session Session) *bosunv1alpha1.AgentSession {
	return &bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: session.CRName, Namespace: session.CRNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "bosun",
				"bosun.io/user":                session.UserID.String(),
				"bosun.io/session":             session.ID.String(),
			},
			Annotations: map[string]string{
				sessionidentity.LastActiveAnnotation: session.CreatedAt.UTC().Format(time.RFC3339),
			},
		},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID: session.ID.String(), UserID: session.UserID.String(),
			DesiredState: bosunv1alpha1.DesiredState(session.DesiredState),
			ResumeNonce:  session.ResumeNonce.String(),
			Tier:         bosunv1alpha1.SessionTier(session.Tier),
			Runtime:      bosunv1alpha1.Runtime(session.Runtime),
			Provider: bosunv1alpha1.ProviderSpec{
				Mode: bosunv1alpha1.ProviderMode(session.Provider.Mode),
			},
			StoragePolicy:         bosunv1alpha1.StoragePolicy(session.StoragePolicy),
			IdleTimeoutSeconds:    1800,
			ActiveDeadlineSeconds: 28800,
			PriorityClassName:     "bosun-free",
		},
	}
}

func allowedTransition(phase bosunv1alpha1.AgentSessionPhase, action Action) bool {
	switch action {
	case ActionHibernate:
		return phase == bosunv1alpha1.AgentSessionPhaseRunning || phase == bosunv1alpha1.AgentSessionPhaseIdle
	case ActionResume:
		return phase == bosunv1alpha1.AgentSessionPhaseHibernating || phase == bosunv1alpha1.AgentSessionPhaseHibernated
	case ActionRetry:
		return phase == bosunv1alpha1.AgentSessionPhaseFailed
	default:
		return false
	}
}

func transitionAlreadyRequested(cr *bosunv1alpha1.AgentSession, action Action) bool {
	switch action {
	case ActionHibernate:
		return cr.Spec.DesiredState == bosunv1alpha1.DesiredStateHibernated
	case ActionResume, ActionRetry:
		return cr.Status.ObservedResumeNonce != cr.Spec.ResumeNonce
	default:
		return true
	}
}

func resourceVersion(cr *bosunv1alpha1.AgentSession) int64 {
	value, err := strconv.ParseInt(cr.ResourceVersion, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

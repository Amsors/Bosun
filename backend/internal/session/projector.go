package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

// Projector watches AgentSession status and maintains the PostgreSQL read projection.
type Projector struct {
	store  Store
	client client.WithWatch
	logger *slog.Logger
	now    func() time.Time
}

const projectionWriteTimeout = 5 * time.Second

func NewProjector(store Store, k8sClient client.WithWatch, logger *slog.Logger) *Projector {
	if logger == nil {
		logger = slog.Default()
	}
	return &Projector{
		store: store, client: k8sClient, logger: logger,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (p *Projector) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := p.runWatch(ctx); err != nil && ctx.Err() == nil {
			p.logger.Error("AgentSession status watch stopped", "reason", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (p *Projector) runWatch(ctx context.Context) error {
	var initial bosunv1alpha1.AgentSessionList
	if err := p.client.List(ctx, &initial); err != nil {
		return fmt.Errorf("list AgentSessions before watch: %w", err)
	}
	for i := range initial.Items {
		if err := p.project(ctx, &initial.Items[i]); err != nil {
			return err
		}
	}
	options := &client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: initial.ResourceVersion}}
	stream, err := p.client.Watch(ctx, &bosunv1alpha1.AgentSessionList{}, options)
	if err != nil {
		return fmt.Errorf("watch AgentSessions: %w", err)
	}
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-stream.ResultChan():
			if !ok {
				return errors.New("AgentSession watch channel closed")
			}
			cr, ok := event.Object.(*bosunv1alpha1.AgentSession)
			if !ok {
				if event.Type == watch.Error {
					return fmt.Errorf("AgentSession watch error: %v", event.Object)
				}
				continue
			}
			if event.Type == watch.Deleted {
				if err := p.cleanupComplete(ctx, cr); err != nil {
					return err
				}
				continue
			}
			if err := p.project(ctx, cr); err != nil {
				return err
			}
		}
	}
}

func (p *Projector) project(ctx context.Context, cr *bosunv1alpha1.AgentSession) error {
	ctx, cancel := context.WithTimeout(ctx, projectionWriteTimeout)
	defer cancel()
	if cr.Status.Phase == "" {
		return nil
	}
	sessionID, err := uuid.Parse(cr.Spec.SessionID)
	if err != nil {
		return nil
	}
	rv, err := strconv.ParseInt(cr.ResourceVersion, 10, 64)
	if err != nil {
		return fmt.Errorf("parse AgentSession %s resourceVersion: %w", cr.Name, err)
	}
	reason := ""
	if condition := apimeta.FindStatusCondition(cr.Status.Conditions, "Ready"); condition != nil {
		reason = condition.Reason
	}
	var active *time.Time
	if cr.Status.LastActiveAt != nil {
		value := cr.Status.LastActiveAt.UTC()
		active = &value
	}
	event, err := newEvent(sessionID, "session.phase_changed", map[string]any{
		"phase": cr.Status.Phase, "reason": reason, "resourceVersion": rv,
	}, p.now())
	if err != nil {
		return err
	}
	_, err = p.store.Project(ctx, Projection{
		SessionID: sessionID, Phase: string(cr.Status.Phase), PhaseReason: reason,
		Conditions: cr.Status.Conditions, LastActiveAt: active,
		ResourceVersion: rv, OccurredAt: event.OccurredAt,
	}, event)
	return err
}

func (p *Projector) cleanupComplete(ctx context.Context, cr *bosunv1alpha1.AgentSession) error {
	ctx, cancel := context.WithTimeout(ctx, projectionWriteTimeout)
	defer cancel()
	sessionID, err := uuid.Parse(cr.Spec.SessionID)
	if err != nil {
		return nil
	}
	event, err := newEvent(sessionID, "session.cleanup_completed", map[string]any{}, p.now())
	if err != nil {
		return err
	}
	return p.store.MarkCleanupComplete(ctx, sessionID, event)
}

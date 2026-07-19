package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	apimeta "k8s.io/apimachinery/pkg/api/meta"

	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/idempotency"
	"github.com/Amsors/Bosun/backend/internal/userenv"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

type Environment interface {
	Phase(context.Context, string) (string, error)
}

type Service struct {
	store   Store
	idem    auth.IdempotencyStore
	env     Environment
	runtime RuntimeControl
	now     func() time.Time
	logger  *slog.Logger
}

type ServiceConfig struct {
	Store       Store
	Idempotency auth.IdempotencyStore
	Environment Environment
	Runtime     RuntimeControl
	Now         func() time.Time
	Logger      *slog.Logger
}

type CreateOutput struct {
	Status int
	Body   []byte
}

const serviceOperationTimeout = 15 * time.Second

func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Store == nil || cfg.Idempotency == nil || cfg.Environment == nil || cfg.Runtime == nil {
		return nil, errors.New("session service requires store, idempotency, environment and runtime control")
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: cfg.Store, idem: cfg.Idempotency, env: cfg.Environment,
		runtime: cfg.Runtime, now: now, logger: logger,
	}, nil
}

func (s *Service) Create(
	ctx context.Context,
	userID uuid.UUID,
	key string,
	method string,
	path string,
	requestHash []byte,
	req CreateRequest,
) (CreateOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	if key == "" || !validCreateRequest(req) {
		return CreateOutput{}, ErrValidation
	}
	var output CreateOutput
	err := s.idem.WithIdempotencyLock(ctx, userID, key, func() error {
		existing, err := s.idem.GetIdempotencyKey(ctx, userID, key)
		if err != nil {
			return fmt.Errorf("get session idempotency key: %w", err)
		}
		switch idempotency.Decide(toIdempotencyRecord(existing), requestHash) {
		case idempotency.Replay:
			output = CreateOutput{Status: existing.Status, Body: existing.Body}
			return nil
		case idempotency.Conflict:
			return ErrIdempotency
		}

		phase, err := s.env.Phase(ctx, userID.String())
		if err != nil {
			return fmt.Errorf("read user environment phase: %w", err)
		}
		switch phase {
		case "Ready":
		case "Failed":
			return ErrEnvironmentFailed
		default:
			return ErrEnvironmentReady
		}
		active, err := s.store.CountActive(ctx, userID)
		if err != nil {
			return err
		}
		if active >= 1 {
			return ErrCapacity
		}
		sessionID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate session ID: %w", err)
		}
		nonce, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate resume nonce: %w", err)
		}
		now := s.now()
		rec := Session{
			ID: sessionID, UserID: userID, CRNamespace: userenv.Namespace(userID.String()),
			CRName: sessionidentity.CRName(sessionID.String()), Tier: req.Tier, Runtime: req.Runtime,
			Provider: Provider{Mode: req.Provider.Mode}, StoragePolicy: req.StoragePolicy,
			DesiredState: "Running", ResumeNonce: nonce, Phase: "Pending",
			PhaseReason: "CreateRequested", Conditions: nil, LastActiveAt: &now,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		body, err := renderSessionEnvelope(rec)
		if err != nil {
			return err
		}
		event, err := newEvent(rec.ID, "session.created", map[string]any{
			"tier": rec.Tier, "runtime": rec.Runtime, "storagePolicy": rec.StoragePolicy,
		}, now)
		if err != nil {
			return err
		}
		if err := s.store.CreateWithEventAndIdempotency(ctx, rec, event, IdempotencyInput{
			Key: key, Method: method, Path: path, RequestHash: requestHash,
			Status: http.StatusAccepted, Body: body, ExpiresAt: now.Add(24 * time.Hour),
		}); err != nil {
			return err
		}
		if err := s.runtime.Ensure(ctx, rec); err != nil {
			s.logger.Error("ensure AgentSession after database commit failed",
				"reason", err, "session_id", rec.ID.String())
		}
		output = CreateOutput{Status: http.StatusAccepted, Body: body}
		return nil
	})
	return output, err
}

func (s *Service) Get(ctx context.Context, userID, sessionID uuid.UUID) (Session, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	rec, err := s.store.Get(ctx, userID, sessionID)
	if err != nil {
		return Session{}, err
	}
	cr, err := s.runtime.Get(ctx, rec)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return rec, nil
		}
		return Session{}, err
	}
	rv := resourceVersion(cr)
	if rv > rec.CRResourceVersion {
		rec.Phase = string(cr.Status.Phase)
		rec.Conditions = cr.Status.Conditions
		if cr.Status.LastActiveAt != nil {
			value := cr.Status.LastActiveAt.UTC()
			rec.LastActiveAt = &value
		}
		if condition := apimeta.FindStatusCondition(cr.Status.Conditions, "Ready"); condition != nil {
			rec.PhaseReason = condition.Reason
		}
	}
	return rec, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, page, pageSize int32) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return Page{}, ErrValidation
	}
	return s.store.List(ctx, userID, pageSize, (page-1)*pageSize)
}

func (s *Service) Transition(
	ctx context.Context,
	userID, sessionID uuid.UUID,
	action Action,
) (Session, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	rec, err := s.store.Get(ctx, userID, sessionID)
	if err != nil {
		return Session{}, err
	}
	nonce := rec.ResumeNonce
	if action == ActionResume || action == ActionRetry {
		nonce, err = uuid.NewV7()
		if err != nil {
			return Session{}, fmt.Errorf("generate transition nonce: %w", err)
		}
	}
	now := s.now()
	if _, err := s.runtime.Mutate(ctx, rec, action, nonce, now); err != nil {
		return Session{}, err
	}
	if action == ActionHibernate {
		rec.DesiredState = "Hibernated"
	} else {
		rec.DesiredState = "Running"
		rec.ResumeNonce = nonce
		rec.LastActiveAt = &now
	}
	rec.UpdatedAt = now
	event, err := newEvent(rec.ID, "session."+string(action)+"_requested", map[string]any{
		"desiredState": rec.DesiredState, "resumeNonce": rec.ResumeNonce.String(),
	}, now)
	if err != nil {
		return Session{}, err
	}
	updated, err := s.store.UpdateDesired(ctx, rec, event)
	if errors.Is(err, ErrConcurrentUpdate) {
		latest, getErr := s.store.Get(ctx, userID, sessionID)
		if getErr == nil && latest.DesiredState == rec.DesiredState && latest.ResumeNonce == rec.ResumeNonce {
			return latest, nil
		}
		return Session{}, ErrInvalidTransition
	}
	return updated, err
}

func (s *Service) Delete(ctx context.Context, userID, sessionID uuid.UUID) (Session, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	now := s.now()
	event, err := newEvent(sessionID, "session.delete_requested", map[string]any{
		"destructive": true,
	}, now)
	if err != nil {
		return Session{}, err
	}
	rec, err := s.store.SoftDelete(ctx, userID, sessionID, event)
	if err != nil {
		return Session{}, err
	}
	if err := s.runtime.Delete(ctx, rec); err != nil {
		s.logger.Error("delete AgentSession after soft delete failed", "reason", err, "session_id", sessionID.String())
	}
	return rec, nil
}

func validCreateRequest(req CreateRequest) bool {
	return (req.Tier == "small" || req.Tier == "medium") &&
		req.Runtime == "claude-code" &&
		req.Provider.Mode == "platform" &&
		req.Provider.CredentialID == "" &&
		req.StoragePolicy == "local"
}

func newEvent(sessionID uuid.UUID, eventType string, payload any, at time.Time) (Event, error) {
	eventID, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("generate session event ID: %w", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal session event payload: %w", err)
	}
	return Event{ID: eventID, SessionID: sessionID, Type: eventType, Payload: body, OccurredAt: at}, nil
}

func renderSessionEnvelope(rec Session) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"code": 0, "message": "ok", "data": toDTO(rec),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal create session response: %w", err)
	}
	return body, nil
}

func toIdempotencyRecord(rec *auth.IdempotencyRecord) *idempotency.Record {
	if rec == nil {
		return nil
	}
	return &idempotency.Record{RequestHash: rec.RequestHash, Status: rec.Status, Body: rec.Body}
}

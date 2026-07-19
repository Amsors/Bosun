package session

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/idempotency"
	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

func TestCreateValidatesEnvironmentAndCapacity(t *testing.T) {
	userID := mustV7(t)
	tests := []struct {
		name   string
		phase  string
		active int64
		want   error
	}{
		{name: "pending environment", phase: "Pending", want: ErrEnvironmentReady},
		{name: "failed environment", phase: "Failed", want: ErrEnvironmentFailed},
		{name: "active capacity", phase: "Ready", active: 1, want: ErrCapacity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			store.active = tt.active
			service := newTestService(t, store, &memoryIdempotency{}, environmentPhase(tt.phase), newMemoryRuntime())
			_, err := service.Create(context.Background(), userID, "key", "POST", "/api/v1/sessions",
				idempotency.RequestHash("POST", "/api/v1/sessions", []byte("body")), validRequest())
			if !errors.Is(err, tt.want) {
				t.Fatalf("Create() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCreateIdempotentlyPersistsSessionEventAndResponseBeforeCR(t *testing.T) {
	userID := mustV7(t)
	store := newMemoryStore()
	idem := &memoryIdempotency{}
	runtime := newMemoryRuntime()
	service := newTestService(t, store, idem, environmentPhase("Ready"), runtime)
	hash := idempotency.RequestHash("POST", "/api/v1/sessions", []byte("same"))

	first, err := service.Create(context.Background(), userID, "key", "POST", "/api/v1/sessions", hash, validRequest())
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	second, err := service.Create(context.Background(), userID, "key", "POST", "/api/v1/sessions", hash, validRequest())
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if first.Status != 202 || second.Status != first.Status || !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("idempotent responses differ: first=%d %s second=%d %s", first.Status, first.Body, second.Status, second.Body)
	}
	if len(store.sessions) != 1 || len(store.events) != 1 || runtime.ensureCalls != 1 {
		t.Fatalf("side effects sessions=%d events=%d ensures=%d", len(store.sessions), len(store.events), runtime.ensureCalls)
	}
	_, err = service.Create(context.Background(), userID, "key", "POST", "/api/v1/sessions",
		idempotency.RequestHash("POST", "/api/v1/sessions", []byte("different")), validRequest())
	if !errors.Is(err, ErrIdempotency) {
		t.Fatalf("conflicting Create() error = %v, want ErrIdempotency", err)
	}
}

func TestTransitionRulesIncludeResumeDuringHibernationAndRetryNonce(t *testing.T) {
	userID := mustV7(t)
	tests := []struct {
		name   string
		phase  bosunv1alpha1.AgentSessionPhase
		action Action
		ok     bool
	}{
		{name: "hibernate running", phase: bosunv1alpha1.AgentSessionPhaseRunning, action: ActionHibernate, ok: true},
		{name: "duplicate hibernate", phase: bosunv1alpha1.AgentSessionPhaseHibernated, action: ActionHibernate},
		{name: "resume hibernating", phase: bosunv1alpha1.AgentSessionPhaseHibernating, action: ActionResume, ok: true},
		{name: "resume hibernated", phase: bosunv1alpha1.AgentSessionPhaseHibernated, action: ActionResume, ok: true},
		{name: "retry failed", phase: bosunv1alpha1.AgentSessionPhaseFailed, action: ActionRetry, ok: true},
		{name: "retry running", phase: bosunv1alpha1.AgentSessionPhaseRunning, action: ActionRetry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			rec := testSession(userID)
			rec.Phase = string(tt.phase)
			store.sessions[rec.ID] = rec
			runtime := newMemoryRuntime()
			runtime.sessions[rec.ID] = agentCR(rec, tt.phase)
			service := newTestService(t, store, &memoryIdempotency{}, environmentPhase("Ready"), runtime)
			before := rec.ResumeNonce
			got, err := service.Transition(context.Background(), userID, rec.ID, tt.action)
			if tt.ok && err != nil {
				t.Fatalf("Transition() error = %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("Transition() error = %v, want ErrInvalidTransition", err)
			}
			if tt.ok && (tt.action == ActionResume || tt.action == ActionRetry) && got.ResumeNonce == before {
				t.Fatal("resume/retry did not generate a new nonce")
			}
		})
	}
}

func TestDuplicateTransitionIsRejectedBeforeStatusCatchesUp(t *testing.T) {
	userID := mustV7(t)
	for _, action := range []Action{ActionHibernate, ActionResume, ActionRetry} {
		t.Run(string(action), func(t *testing.T) {
			store := newMemoryStore()
			rec := testSession(userID)
			switch action {
			case ActionHibernate:
				rec.Phase = string(bosunv1alpha1.AgentSessionPhaseRunning)
			case ActionResume:
				rec.Phase = string(bosunv1alpha1.AgentSessionPhaseHibernated)
				rec.DesiredState = "Running"
			case ActionRetry:
				rec.Phase = string(bosunv1alpha1.AgentSessionPhaseFailed)
			}
			store.sessions[rec.ID] = rec
			runtime := newMemoryRuntime()
			runtime.sessions[rec.ID] = agentCR(rec, bosunv1alpha1.AgentSessionPhase(rec.Phase))
			service := newTestService(t, store, &memoryIdempotency{}, environmentPhase("Ready"), runtime)
			if _, err := service.Transition(context.Background(), userID, rec.ID, action); err != nil {
				t.Fatalf("first Transition() error = %v", err)
			}
			if _, err := service.Transition(context.Background(), userID, rec.ID, action); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("duplicate Transition() error = %v, want ErrInvalidTransition", err)
			}
		})
	}
}

func TestProjectorRejectsOutOfOrderResourceVersion(t *testing.T) {
	store := newMemoryStore()
	rec := testSession(mustV7(t))
	store.sessions[rec.ID] = rec
	projector := &Projector{store: store, now: func() time.Time { return time.Unix(10, 0).UTC() }}
	newer := agentCR(rec, bosunv1alpha1.AgentSessionPhaseRunning)
	newer.ResourceVersion = "10"
	if err := projector.project(context.Background(), newer); err != nil {
		t.Fatalf("newer projection error = %v", err)
	}
	older := agentCR(rec, bosunv1alpha1.AgentSessionPhaseProvisioning)
	older.ResourceVersion = "9"
	if err := projector.project(context.Background(), older); err != nil {
		t.Fatalf("older projection error = %v", err)
	}
	if got := store.sessions[rec.ID].Phase; got != "Running" {
		t.Fatalf("phase regressed to %q", got)
	}
	if len(store.events) != 1 {
		t.Fatalf("projected events = %d, want 1", len(store.events))
	}
}

func TestRepairerEnsuresPendingAndRetriesDeletingCRs(t *testing.T) {
	store := newMemoryStore()
	pending := testSession(mustV7(t))
	deleting := testSession(mustV7(t))
	deleting.ID = mustV7(t)
	deleting.Phase = "Deleting"
	now := time.Now().UTC()
	deleting.DeletedAt = &now
	store.sessions[pending.ID] = pending
	store.sessions[deleting.ID] = deleting
	runtime := newMemoryRuntime()
	repairer := NewRepairer(store, runtime, time.Minute, nil)
	if err := repairer.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if runtime.ensureCalls != 1 || runtime.deleteCalls != 1 {
		t.Fatalf("repair calls ensure=%d delete=%d", runtime.ensureCalls, runtime.deleteCalls)
	}
}

func TestSessionOwnershipIsEnforcedByEveryStoreLookup(t *testing.T) {
	ownerID := mustV7(t)
	otherID := mustV7(t)
	store := newMemoryStore()
	rec := testSession(ownerID)
	store.sessions[rec.ID] = rec
	service := newTestService(t, store, &memoryIdempotency{}, environmentPhase("Ready"), newMemoryRuntime())
	if _, err := service.Get(context.Background(), otherID, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Get() error = %v, want ErrNotFound", err)
	}
	if _, err := service.Transition(context.Background(), otherID, rec.ID, ActionHibernate); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Transition() error = %v, want ErrNotFound", err)
	}
	if _, err := service.Delete(context.Background(), otherID, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Delete() error = %v, want ErrNotFound", err)
	}
}

type environmentPhase string

func (e environmentPhase) Phase(context.Context, string) (string, error) { return string(e), nil }

type memoryIdempotency struct {
	mu      sync.Mutex
	records map[string]*auth.IdempotencyRecord
}

func (m *memoryIdempotency) WithIdempotencyLock(_ context.Context, _ uuid.UUID, _ string, fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn()
}
func (m *memoryIdempotency) GetIdempotencyKey(_ context.Context, scope uuid.UUID, key string) (*auth.IdempotencyRecord, error) {
	if m.records == nil {
		return nil, nil
	}
	return m.records[scope.String()+"|"+key], nil
}
func (m *memoryIdempotency) InsertIdempotencyKey(_ context.Context, rec auth.IdempotencyRecord) (bool, error) {
	if m.records == nil {
		m.records = make(map[string]*auth.IdempotencyRecord)
	}
	k := rec.Scope.String() + "|" + rec.Key
	if m.records[k] != nil {
		return false, nil
	}
	copy := rec
	m.records[k] = &copy
	return true, nil
}

type memoryStore struct {
	sessions map[uuid.UUID]Session
	events   []Event
	active   int64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sessions: make(map[uuid.UUID]Session)}
}
func (m *memoryStore) CreateWithEventAndIdempotency(_ context.Context, rec Session, event Event, idem IdempotencyInput) error {
	m.sessions[rec.ID] = rec
	m.events = append(m.events, event)
	// Service and store share the idempotency backend in production. Tests copy
	// the persisted response through the service's configured in-memory store.
	return nil
}
func (m *memoryStore) CountActive(context.Context, uuid.UUID) (int64, error) { return m.active, nil }
func (m *memoryStore) Get(_ context.Context, userID, id uuid.UUID) (Session, error) {
	rec, ok := m.sessions[id]
	if !ok || rec.UserID != userID || rec.DeletedAt != nil {
		return Session{}, ErrNotFound
	}
	return rec, nil
}
func (m *memoryStore) List(_ context.Context, userID uuid.UUID, limit, offset int32) (Page, error) {
	items := make([]Session, 0)
	for _, rec := range m.sessions {
		if rec.UserID == userID && rec.DeletedAt == nil {
			items = append(items, rec)
		}
	}
	return Page{Items: items, Total: int64(len(items))}, nil
}
func (m *memoryStore) UpdateDesired(_ context.Context, rec Session, event Event) (Session, error) {
	current := m.sessions[rec.ID]
	if current.Version != rec.Version {
		return Session{}, ErrConcurrentUpdate
	}
	rec.Version++
	m.sessions[rec.ID] = rec
	m.events = append(m.events, event)
	return rec, nil
}
func (m *memoryStore) SoftDelete(_ context.Context, userID, id uuid.UUID, event Event) (Session, error) {
	rec, err := m.Get(context.Background(), userID, id)
	if err != nil {
		return Session{}, err
	}
	rec.Phase = "Deleting"
	rec.PhaseReason = "DeleteRequested"
	rec.DeletedAt = &event.OccurredAt
	m.sessions[id] = rec
	m.events = append(m.events, event)
	return rec, nil
}
func (m *memoryStore) Project(_ context.Context, projection Projection, event Event) (bool, error) {
	rec, ok := m.sessions[projection.SessionID]
	if !ok || rec.CRResourceVersion >= projection.ResourceVersion {
		return false, nil
	}
	rec.Phase = projection.Phase
	rec.PhaseReason = projection.PhaseReason
	rec.Conditions = projection.Conditions
	rec.CRResourceVersion = projection.ResourceVersion
	m.sessions[rec.ID] = rec
	m.events = append(m.events, event)
	return true, nil
}
func (m *memoryStore) MarkCleanupComplete(context.Context, uuid.UUID, Event) error { return nil }
func (m *memoryStore) ListPending(context.Context, int32) ([]Session, error) {
	var result []Session
	for _, rec := range m.sessions {
		if rec.Phase == "Pending" && rec.DeletedAt == nil {
			result = append(result, rec)
		}
	}
	return result, nil
}
func (m *memoryStore) ListDeleting(context.Context, int32) ([]Session, error) {
	var result []Session
	for _, rec := range m.sessions {
		if rec.DeletedAt != nil && rec.PhaseReason != "CleanupComplete" {
			result = append(result, rec)
		}
	}
	return result, nil
}

type memoryRuntime struct {
	sessions    map[uuid.UUID]*bosunv1alpha1.AgentSession
	ensureCalls int
	deleteCalls int
}

func newMemoryRuntime() *memoryRuntime {
	return &memoryRuntime{sessions: make(map[uuid.UUID]*bosunv1alpha1.AgentSession)}
}
func (m *memoryRuntime) Ensure(_ context.Context, rec Session) error {
	m.ensureCalls++
	m.sessions[rec.ID] = agentCR(rec, bosunv1alpha1.AgentSessionPhasePending)
	return nil
}
func (m *memoryRuntime) Get(_ context.Context, rec Session) (*bosunv1alpha1.AgentSession, error) {
	cr := m.sessions[rec.ID]
	if cr == nil {
		return nil, ErrNotFound
	}
	return cr.DeepCopy(), nil
}
func (m *memoryRuntime) Mutate(_ context.Context, rec Session, action Action, nonce uuid.UUID, now time.Time) (*bosunv1alpha1.AgentSession, error) {
	cr := m.sessions[rec.ID]
	if cr == nil {
		return nil, ErrNotFound
	}
	if !allowedTransition(cr.Status.Phase, action) || transitionAlreadyRequested(cr, action) {
		return nil, ErrInvalidTransition
	}
	if action == ActionHibernate {
		cr.Spec.DesiredState = bosunv1alpha1.DesiredStateHibernated
	} else {
		cr.Spec.DesiredState = bosunv1alpha1.DesiredStateRunning
		cr.Spec.ResumeNonce = nonce.String()
	}
	return cr.DeepCopy(), nil
}
func (m *memoryRuntime) Delete(_ context.Context, rec Session) error {
	m.deleteCalls++
	delete(m.sessions, rec.ID)
	return nil
}

func newTestService(
	t *testing.T,
	store *memoryStore,
	idem *memoryIdempotency,
	env Environment,
	runtime *memoryRuntime,
) *Service {
	t.Helper()
	// Mirror CreateWithEventAndIdempotency's atomic idempotency write.
	originalStore := store
	wrapped := &idempotentMemoryStore{memoryStore: originalStore, idem: idem}
	service, err := NewService(ServiceConfig{
		Store: wrapped, Idempotency: idem, Environment: env, Runtime: runtime,
		Now: func() time.Time { return time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

type idempotentMemoryStore struct {
	*memoryStore
	idem *memoryIdempotency
}

func (m *idempotentMemoryStore) CreateWithEventAndIdempotency(ctx context.Context, rec Session, event Event, input IdempotencyInput) error {
	if err := m.memoryStore.CreateWithEventAndIdempotency(ctx, rec, event, input); err != nil {
		return err
	}
	_, err := m.idem.InsertIdempotencyKey(ctx, auth.IdempotencyRecord{
		Scope: rec.UserID, Key: input.Key, Method: input.Method, Path: input.Path,
		RequestHash: input.RequestHash, Status: input.Status, Body: input.Body, ExpiresAt: input.ExpiresAt,
	})
	return err
}

func validRequest() CreateRequest {
	return CreateRequest{
		Tier: "small", Runtime: "claude-code",
		Provider: ProviderRequest{Mode: "platform"}, StoragePolicy: "local",
	}
}

func testSession(userID uuid.UUID) Session {
	id, _ := uuid.NewV7()
	nonce, _ := uuid.NewV7()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	return Session{
		ID: id, UserID: userID, CRNamespace: "bosun-u-test", CRName: "sess-test",
		Tier: "small", Runtime: "claude-code", Provider: Provider{Mode: "platform"},
		StoragePolicy: "local", DesiredState: "Running", ResumeNonce: nonce,
		Phase: "Pending", CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func agentCR(rec Session, phase bosunv1alpha1.AgentSessionPhase) *bosunv1alpha1.AgentSession {
	cr := &bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: rec.CRName, Namespace: rec.CRNamespace, ResourceVersion: "1",
		},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID: rec.ID.String(), UserID: rec.UserID.String(),
			DesiredState: bosunv1alpha1.DesiredState(rec.DesiredState), ResumeNonce: rec.ResumeNonce.String(),
		},
		Status: bosunv1alpha1.AgentSessionStatus{Phase: phase},
	}
	cr.Status.ObservedResumeNonce = rec.ResumeNonce.String()
	return cr
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7() error = %v", err)
	}
	return id
}

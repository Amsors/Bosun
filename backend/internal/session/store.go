package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	db "github.com/Amsors/Bosun/backend/internal/database/sqlc"
)

type Store interface {
	CreateWithEventAndIdempotency(context.Context, Session, Event, IdempotencyInput) error
	CountTotal(context.Context, uuid.UUID) (int64, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (Session, error)
	GetByID(context.Context, uuid.UUID) (Session, error)
	List(context.Context, uuid.UUID, int32, int32) (Page, error)
	UpdateDesired(context.Context, Session, Event) (Session, error)
	SoftDelete(context.Context, uuid.UUID, uuid.UUID, Event) (Session, error)
	Project(context.Context, Projection, Event) (bool, error)
	MarkCleanupComplete(context.Context, uuid.UUID, Event) error
	ListPending(context.Context, int32) ([]Session, error)
	ListDeleting(context.Context, int32) ([]Session, error)
}

type PgxStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewPgxStore(pool *pgxpool.Pool) *PgxStore {
	return &PgxStore{pool: pool, q: db.New(pool)}
}

func (s *PgxStore) CreateWithEventAndIdempotency(
	ctx context.Context,
	session Session,
	event Event,
	idem IdempotencyInput,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create session transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", session.UserID.String()+":active-session"); err != nil {
		return fmt.Errorf("lock active session capacity: %w", err)
	}
	total, err := qtx.CountSessionsForUser(ctx, session.UserID)
	if err != nil {
		return fmt.Errorf("recheck session capacity: %w", err)
	}
	if total >= MaxSessionsPerUser {
		return ErrCapacity
	}
	conditions, err := json.Marshal(session.Conditions)
	if err != nil {
		return fmt.Errorf("marshal initial conditions: %w", err)
	}
	if _, err := qtx.CreateSession(ctx, db.CreateSessionParams{
		ID: session.ID, UserID: session.UserID, CrNamespace: session.CRNamespace, CrName: session.CRName,
		DisplayName: session.Name, Priority: session.Priority,
		Tier: session.Tier, Runtime: session.Runtime, ProviderMode: session.Provider.Mode,
		ProviderCredentialID: session.Provider.CredentialID, StoragePolicy: session.StoragePolicy,
		DesiredState: session.DesiredState, ResumeNonce: session.ResumeNonce, Phase: session.Phase,
		PhaseReason: session.PhaseReason, Conditions: conditions, LastActiveAt: session.LastActiveAt,
		CreatedAt: session.CreatedAt,
	}); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	if err := insertEvent(ctx, qtx, event); err != nil {
		return err
	}
	rows, err := qtx.InsertIdempotencyKey(ctx, db.InsertIdempotencyKeyParams{
		UserID: session.UserID, Key: idem.Key, Method: idem.Method, Path: idem.Path,
		RequestHash: idem.RequestHash, ResponseStatus: int32(idem.Status), ResponseBody: idem.Body,
		ExpiresAt: idem.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("insert session idempotency response: %w", err)
	}
	if rows != 1 {
		return errors.New("active idempotency key appeared while lock was held")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create session transaction: %w", err)
	}
	return nil
}

func (s *PgxStore) CountTotal(ctx context.Context, userID uuid.UUID) (int64, error) {
	count, err := s.q.CountSessionsForUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("count sessions: %w", err)
	}
	return count, nil
}

func (s *PgxStore) Get(ctx context.Context, userID, sessionID uuid.UUID) (Session, error) {
	row, err := s.q.GetSessionForUser(ctx, db.GetSessionForUserParams{ID: sessionID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	rec, err := sessionFromRow(row)
	if err != nil {
		return Session{}, err
	}
	return rec, nil
}

// GetByID returns a session regardless of owner so terminal authorization can
// distinguish a missing session from a cross-user access attempt.
func (s *PgxStore) GetByID(ctx context.Context, sessionID uuid.UUID) (Session, error) {
	row, err := s.q.GetSessionByID(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.DeletedAt != nil) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session by ID: %w", err)
	}
	rec, err := sessionFromRow(row)
	if err != nil {
		return Session{}, err
	}
	return rec, nil
}

func (s *PgxStore) List(ctx context.Context, userID uuid.UUID, limit, offset int32) (Page, error) {
	rows, err := s.q.ListSessionsForUser(ctx, db.ListSessionsForUserParams{
		UserID: userID, Limit: limit, Offset: offset,
	})
	if err != nil {
		return Page{}, fmt.Errorf("list sessions: %w", err)
	}
	items := make([]Session, 0, len(rows))
	for _, row := range rows {
		item, err := sessionFromRow(row)
		if err != nil {
			return Page{}, err
		}
		items = append(items, item)
	}
	total, err := s.q.CountSessionsForUser(ctx, userID)
	if err != nil {
		return Page{}, fmt.Errorf("count sessions: %w", err)
	}
	return Page{Items: items, Total: total}, nil
}

func (s *PgxStore) UpdateDesired(ctx context.Context, session Session, event Event) (Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin desired state transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.UpdateSessionDesiredState(ctx, db.UpdateSessionDesiredStateParams{
		ID: session.ID, UserID: session.UserID, DesiredState: session.DesiredState,
		ResumeNonce: session.ResumeNonce, LastActiveAt: session.LastActiveAt,
		UpdatedAt: session.UpdatedAt, Version: session.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrConcurrentUpdate
	}
	if err != nil {
		return Session{}, fmt.Errorf("update session desired state: %w", err)
	}
	if err := insertEvent(ctx, qtx, event); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit desired state transaction: %w", err)
	}
	rec, err := sessionFromRow(row)
	if err != nil {
		return Session{}, err
	}
	return rec, nil
}

func (s *PgxStore) SoftDelete(ctx context.Context, userID, sessionID uuid.UUID, event Event) (Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin delete session transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	deletedAt := event.OccurredAt
	row, err := qtx.SoftDeleteSession(ctx, db.SoftDeleteSessionParams{
		ID: sessionID, UserID: userID, DeletedAt: &deletedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("soft delete session: %w", err)
	}
	if err := insertEvent(ctx, qtx, event); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit delete session transaction: %w", err)
	}
	rec, err := sessionFromRow(row)
	if err != nil {
		return Session{}, err
	}
	return rec, nil
}

func (s *PgxStore) Project(ctx context.Context, projection Projection, event Event) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin status projection transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	conditions, err := json.Marshal(projection.Conditions)
	if err != nil {
		return false, fmt.Errorf("marshal projected conditions: %w", err)
	}
	_, err = qtx.ProjectSessionStatus(ctx, db.ProjectSessionStatusParams{
		ID: projection.SessionID, Phase: projection.Phase, PhaseReason: projection.PhaseReason,
		Conditions: conditions, LastActiveAt: projection.LastActiveAt,
		CrResourceVersion: projection.ResourceVersion, UpdatedAt: projection.OccurredAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("project session status: %w", err)
	}
	if err := insertEvent(ctx, qtx, event); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit status projection transaction: %w", err)
	}
	return true, nil
}

func (s *PgxStore) MarkCleanupComplete(ctx context.Context, sessionID uuid.UUID, event Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cleanup projection transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	rows, err := qtx.MarkSessionCleanupComplete(ctx, db.MarkSessionCleanupCompleteParams{
		ID: sessionID, UpdatedAt: event.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("mark session cleanup complete: %w", err)
	}
	if rows == 0 {
		return nil
	}
	if err := insertEvent(ctx, qtx, event); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit cleanup projection transaction: %w", err)
	}
	return nil
}

func (s *PgxStore) ListPending(ctx context.Context, limit int32) ([]Session, error) {
	rows, err := s.q.ListPendingSessions(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending sessions: %w", err)
	}
	return sessionsFromRows(rows)
}

func (s *PgxStore) ListDeleting(ctx context.Context, limit int32) ([]Session, error) {
	rows, err := s.q.ListDeletingSessions(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list deleting sessions: %w", err)
	}
	return sessionsFromRows(rows)
}

func insertEvent(ctx context.Context, q *db.Queries, event Event) error {
	if err := q.InsertSessionEvent(ctx, db.InsertSessionEventParams{
		ID: event.ID, SessionID: event.SessionID, Type: event.Type,
		Payload: event.Payload, OccurredAt: event.OccurredAt,
	}); err != nil {
		return fmt.Errorf("insert session event %s: %w", event.Type, err)
	}
	return nil
}

func sessionFromRow(row db.BosunSession) (Session, error) {
	var conditions []metav1.Condition
	if err := json.Unmarshal(row.Conditions, &conditions); err != nil {
		return Session{}, fmt.Errorf("decode session %s conditions: %w", row.ID, err)
	}
	return Session{
		ID: row.ID, UserID: row.UserID, Name: row.DisplayName, Priority: row.Priority,
		CRNamespace: row.CrNamespace, CRName: row.CrName,
		Tier: row.Tier, Runtime: row.Runtime, Provider: Provider{Mode: row.ProviderMode, CredentialID: row.ProviderCredentialID},
		StoragePolicy: row.StoragePolicy, DesiredState: row.DesiredState, ResumeNonce: row.ResumeNonce,
		Phase: row.Phase, PhaseReason: row.PhaseReason, Conditions: conditions,
		LastActiveAt: row.LastActiveAt, CRResourceVersion: row.CrResourceVersion,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt, Version: row.Version,
	}, nil
}

func sessionsFromRows(rows []db.BosunSession) ([]Session, error) {
	sessions := make([]Session, 0, len(rows))
	for _, row := range rows {
		item, err := sessionFromRow(row)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, item)
	}
	return sessions, nil
}

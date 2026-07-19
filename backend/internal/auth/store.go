package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/Amsors/Bosun/backend/internal/database/sqlc"
)

const uniqueViolation = "23505"

const idempotencyUnlockTimeout = 5 * time.Second

// PgxStore 用 pgx + sqlc 实现 Store。事务性操作在此集中，业务逻辑在 Service。
type PgxStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// NewPgxStore 基于连接池构造 store。
func NewPgxStore(pool *pgxpool.Pool) *PgxStore {
	return &PgxStore{pool: pool, q: db.New(pool)}
}

// CreateUser 创建用户；邮箱唯一冲突映射为 ErrEmailTaken。
func (s *PgxStore) CreateUser(ctx context.Context, id uuid.UUID, email, passwordHash string) (User, error) {
	row, err := s.q.CreateUser(ctx, db.CreateUserParams{ID: id, Email: email, PasswordHash: passwordHash})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return userFromRow(row), nil
}

func (s *PgxStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("get user by email: %w", err)
	}
	return userFromRow(row), nil
}

func (s *PgxStore) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("get user by id: %w", err)
	}
	return userFromRow(row), nil
}

func (s *PgxStore) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	if err := s.q.UpdateUserPasswordHash(ctx, db.UpdateUserPasswordHashParams{ID: id, PasswordHash: hash}); err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	return nil
}

func (s *PgxStore) ListUserIDs(ctx context.Context) ([]string, error) {
	ids, err := s.q.ListUserIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list user ids: %w", err)
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out, nil
}

func (s *PgxStore) InsertRefreshToken(ctx context.Context, rec RefreshTokenRecord) error {
	if err := s.q.InsertRefreshToken(ctx, insertParams(rec)); err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}

func (s *PgxStore) GetRefreshTokenByHash(ctx context.Context, hash []byte) (RefreshTokenRecord, error) {
	row, err := s.q.GetRefreshTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RefreshTokenRecord{}, ErrRefreshTokenNotFound
		}
		return RefreshTokenRecord{}, fmt.Errorf("get refresh token: %w", err)
	}
	return RefreshTokenRecord{
		ID:         row.ID,
		UserID:     row.UserID,
		FamilyID:   row.FamilyID,
		TokenHash:  row.TokenHash,
		ExpiresAt:  row.ExpiresAt,
		RevokedAt:  row.RevokedAt,
		ReplacedBy: row.ReplacedBy,
	}, nil
}

// RotateRefreshToken 在单个事务内原子撤销旧 token 并插入替换 token；旧 token 已被撤销时返回 won=false。
func (s *PgxStore) RotateRefreshToken(ctx context.Context, oldID uuid.UUID, replacement RefreshTokenRecord) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin rotation tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	// 先插入替换 token，使旧 token 的 replaced_by 外键有效；落败时由 defer 的 Rollback 撤销此插入。
	if err := qtx.InsertRefreshToken(ctx, insertParams(replacement)); err != nil {
		return false, fmt.Errorf("insert rotated token: %w", err)
	}
	replacedBy := replacement.ID
	if _, err := qtx.RotateRefreshToken(ctx, db.RotateRefreshTokenParams{ID: oldID, ReplacedBy: &replacedBy}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // 已被并发轮换或撤销，回滚撤销刚插入的替换 token
		}
		return false, fmt.Errorf("rotate old token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit rotation: %w", err)
	}
	return true, nil
}

func (s *PgxStore) RevokeRefreshTokenFamily(ctx context.Context, familyID uuid.UUID) error {
	if err := s.q.RevokeRefreshTokenFamily(ctx, familyID); err != nil {
		return fmt.Errorf("revoke refresh token family: %w", err)
	}
	return nil
}

// WithIdempotencyLock serializes the complete side-effect and response-recording window for one key.
// A PostgreSQL session advisory lock works across API replicas and is released if the connection closes.
func (s *PgxStore) WithIdempotencyLock(
	ctx context.Context,
	scope uuid.UUID,
	key string,
	fn func() error,
) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire idempotency lock connection: %w", err)
	}
	lockInput := scope.String() + ":" + key
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", lockInput); err != nil {
		conn.Release()
		return fmt.Errorf("acquire idempotency lock: %w", err)
	}

	callbackErr := fn()
	unlockCtx, cancel := context.WithTimeout(context.Background(), idempotencyUnlockTimeout)
	defer cancel()
	if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", lockInput); err != nil {
		hijacked := conn.Hijack()
		_ = hijacked.Close(unlockCtx)
		if callbackErr != nil {
			return callbackErr
		}
		return fmt.Errorf("release idempotency lock: %w", err)
	}
	conn.Release()
	return callbackErr
}

func (s *PgxStore) GetIdempotencyKey(ctx context.Context, scope uuid.UUID, key string) (*IdempotencyRecord, error) {
	row, err := s.q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{UserID: scope, Key: key})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get idempotency key: %w", err)
	}
	return &IdempotencyRecord{
		Scope:       row.UserID,
		Key:         row.Key,
		Method:      row.Method,
		Path:        row.Path,
		RequestHash: row.RequestHash,
		Status:      int(row.ResponseStatus),
		Body:        row.ResponseBody,
		ExpiresAt:   row.ExpiresAt,
	}, nil
}

// InsertIdempotencyKey 插入幂等记录；主键冲突（并发同键）时返回 inserted=false。
func (s *PgxStore) InsertIdempotencyKey(ctx context.Context, rec IdempotencyRecord) (bool, error) {
	rows, err := s.q.InsertIdempotencyKey(ctx, db.InsertIdempotencyKeyParams{
		UserID:         rec.Scope,
		Key:            rec.Key,
		Method:         rec.Method,
		Path:           rec.Path,
		RequestHash:    rec.RequestHash,
		ResponseStatus: int32(rec.Status),
		ResponseBody:   rec.Body,
		ExpiresAt:      rec.ExpiresAt,
	})
	if err != nil {
		return false, fmt.Errorf("insert idempotency key: %w", err)
	}
	return rows == 1, nil
}

func userFromRow(row db.BosunUser) User {
	return User{
		ID:           row.ID,
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		CreatedAt:    row.CreatedAt,
		DisabledAt:   row.DisabledAt,
	}
}

func insertParams(rec RefreshTokenRecord) db.InsertRefreshTokenParams {
	return db.InsertRefreshTokenParams{
		ID:        rec.ID,
		UserID:    rec.UserID,
		FamilyID:  rec.FamilyID,
		TokenHash: rec.TokenHash,
		ExpiresAt: rec.ExpiresAt,
	}
}

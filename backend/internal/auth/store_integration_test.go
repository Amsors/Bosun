package auth

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Amsors/Bosun/backend/internal/database"
)

// 集成测试针对真实 PostgreSQL，验证 migration、sqlc 查询与事务语义。
// 设置 BOSUN_TEST_DATABASE_URL 指向可写库后运行；未设置则跳过（techspec §11.1）。

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("BOSUN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("BOSUN_TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	applyMigrations(t, pool, url)
	return pool
}

func applyMigrations(t *testing.T, pool *pgxpool.Pool, databaseURL string) {
	t.Helper()
	ctx := context.Background()
	// 每次从干净状态开始，避免跨用例污染。
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS bosun CASCADE; DROP TABLE IF EXISTS public.schema_migrations"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := database.Migrate(ctx, databaseURL, 10*time.Second); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := database.Migrate(ctx, databaseURL, 10*time.Second); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
}

func TestStoreUserUniquenessAndCaseInsensitivity(t *testing.T) {
	store := NewPgxStore(integrationPool(t))
	ctx := context.Background()
	id1, _ := uuid.NewV7()
	if _, err := store.CreateUser(ctx, id1, "user@example.com", "hash"); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	// 大小写不同视为同一邮箱：唯一冲突。
	id2, _ := uuid.NewV7()
	if _, err := store.CreateUser(ctx, id2, "User@Example.com", "hash"); err != ErrEmailTaken {
		t.Fatalf("duplicate error = %v, want ErrEmailTaken", err)
	}
	got, err := store.GetUserByEmail(ctx, "USER@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("GetUserByEmail() error = %v", err)
	}
	if got.ID != id1 {
		t.Fatalf("case-insensitive lookup returned %v, want %v", got.ID, id1)
	}
}

func TestStoreRefreshRotationSingleWinner(t *testing.T) {
	store := NewPgxStore(integrationPool(t))
	ctx := context.Background()
	userID, _ := uuid.NewV7()
	if _, err := store.CreateUser(ctx, userID, "a@b.co", "hash"); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	familyID, _ := uuid.NewV7()
	oldID, _ := uuid.NewV7()
	if err := store.InsertRefreshToken(ctx, RefreshTokenRecord{
		ID: oldID, UserID: userID, FamilyID: familyID,
		TokenHash: HashRefreshToken("old"), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("InsertRefreshToken() error = %v", err)
	}

	const n = 6
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			newID, _ := uuid.NewV7()
			won, err := store.RotateRefreshToken(ctx, oldID, RefreshTokenRecord{
				ID: newID, UserID: userID, FamilyID: familyID,
				TokenHash: HashRefreshToken(uuid.NewString()), ExpiresAt: time.Now().Add(time.Hour),
			})
			if err != nil {
				return
			}
			if won {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("rotation winners = %d, want exactly 1", wins)
	}
}

func TestStoreRevokeFamily(t *testing.T) {
	store := NewPgxStore(integrationPool(t))
	ctx := context.Background()
	userID, _ := uuid.NewV7()
	if _, err := store.CreateUser(ctx, userID, "a@b.co", "hash"); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	familyID, _ := uuid.NewV7()
	tokenID, _ := uuid.NewV7()
	if err := store.InsertRefreshToken(ctx, RefreshTokenRecord{
		ID: tokenID, UserID: userID, FamilyID: familyID,
		TokenHash: HashRefreshToken("tok"), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("InsertRefreshToken() error = %v", err)
	}
	if err := store.RevokeRefreshTokenFamily(ctx, familyID); err != nil {
		t.Fatalf("RevokeRefreshTokenFamily() error = %v", err)
	}
	rec, err := store.GetRefreshTokenByHash(ctx, HashRefreshToken("tok"))
	if err != nil {
		t.Fatalf("GetRefreshTokenByHash() error = %v", err)
	}
	if rec.RevokedAt == nil {
		t.Fatal("token should be revoked after family revocation")
	}
}

func TestStoreIdempotency(t *testing.T) {
	store := NewPgxStore(integrationPool(t))
	ctx := context.Background()
	rec := IdempotencyRecord{
		Scope: uuid.Nil, Key: "k1", Method: "POST", Path: "/api/v1/auth/register",
		RequestHash: []byte("hash"), Status: 200, Body: []byte(`{"code":0}`),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	inserted, err := store.InsertIdempotencyKey(ctx, rec)
	if err != nil || !inserted {
		t.Fatalf("first insert inserted=%v err=%v", inserted, err)
	}
	// 同键重复插入应被主键冲突拦截（inserted=false）。
	inserted, err = store.InsertIdempotencyKey(ctx, rec)
	if err != nil || inserted {
		t.Fatalf("second insert inserted=%v err=%v, want false/nil", inserted, err)
	}
	got, err := store.GetIdempotencyKey(ctx, uuid.Nil, "k1")
	if err != nil || got == nil {
		t.Fatalf("GetIdempotencyKey() got=%v err=%v", got, err)
	}
	if !strings.Contains(string(got.Body), `"code":0`) {
		t.Fatalf("stored body = %s", got.Body)
	}

	expired := rec
	expired.Key = "expired"
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	inserted, err = store.InsertIdempotencyKey(ctx, expired)
	if err != nil || !inserted {
		t.Fatalf("insert expired record inserted=%v err=%v", inserted, err)
	}
	got, err = store.GetIdempotencyKey(ctx, uuid.Nil, expired.Key)
	if err != nil || got != nil {
		t.Fatalf("expired GetIdempotencyKey() got=%v err=%v, want nil/nil", got, err)
	}
	replacement := expired
	replacement.RequestHash = []byte("replacement")
	replacement.ExpiresAt = time.Now().Add(time.Hour)
	inserted, err = store.InsertIdempotencyKey(ctx, replacement)
	if err != nil || !inserted {
		t.Fatalf("replace expired record inserted=%v err=%v", inserted, err)
	}
	got, err = store.GetIdempotencyKey(ctx, uuid.Nil, expired.Key)
	if err != nil || got == nil || string(got.RequestHash) != "replacement" {
		t.Fatalf("replacement GetIdempotencyKey() got=%v err=%v", got, err)
	}
}

func TestStoreIdempotencyLockSerializesSameKey(t *testing.T) {
	store := NewPgxStore(integrationPool(t))
	ctx := context.Background()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	errs := make(chan error, 2)

	go func() {
		errs <- store.WithIdempotencyLock(ctx, uuid.Nil, "same-key", func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case err := <-errs:
		t.Fatalf("first WithIdempotencyLock() error = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first callback did not enter")
	}
	go func() {
		errs <- store.WithIdempotencyLock(ctx, uuid.Nil, "same-key", func() error {
			close(secondEntered)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("second callback entered before first lock was released")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second callback did not enter after first lock was released")
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("WithIdempotencyLock() error = %v", err)
		}
	}
}

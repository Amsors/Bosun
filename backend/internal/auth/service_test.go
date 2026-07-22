package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeStore 是内存版 Store，RotateRefreshToken 用互斥锁模拟数据库层的单胜出原子性。
type fakeStore struct {
	mu            sync.Mutex
	usersByID     map[uuid.UUID]User
	emailToID     map[string]uuid.UUID
	refreshByHash map[string]*RefreshTokenRecord
	refreshByID   map[uuid.UUID]*RefreshTokenRecord
	idem          map[string]*IdempotencyRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		usersByID:     map[uuid.UUID]User{},
		emailToID:     map[string]uuid.UUID{},
		refreshByHash: map[string]*RefreshTokenRecord{},
		refreshByID:   map[uuid.UUID]*RefreshTokenRecord{},
		idem:          map[string]*IdempotencyRecord{},
	}
}

func (f *fakeStore) CreateUser(_ context.Context, id uuid.UUID, email, hash string) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.emailToID[email]; ok {
		return User{}, ErrEmailTaken
	}
	u := User{ID: id, Email: email, PasswordHash: hash, CreatedAt: time.Unix(0, 0)}
	f.usersByID[id] = u
	f.emailToID[email] = id
	return u, nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.emailToID[email]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return f.usersByID[id], nil
}

func (f *fakeStore) GetUserByID(_ context.Context, id uuid.UUID) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.usersByID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (f *fakeStore) UpdatePasswordHash(_ context.Context, id uuid.UUID, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.usersByID[id]
	u.PasswordHash = hash
	f.usersByID[id] = u
	return nil
}

func (f *fakeStore) ListUserIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.usersByID))
	for id := range f.usersByID {
		out = append(out, id.String())
	}
	return out, nil
}

func (f *fakeStore) InsertRefreshToken(_ context.Context, rec RefreshTokenRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putLocked(rec)
	return nil
}

func (f *fakeStore) putLocked(rec RefreshTokenRecord) {
	copyRec := rec
	f.refreshByHash[hex.EncodeToString(rec.TokenHash)] = &copyRec
	f.refreshByID[rec.ID] = &copyRec
}

func (f *fakeStore) GetRefreshTokenByHash(_ context.Context, hash []byte) (RefreshTokenRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.refreshByHash[hex.EncodeToString(hash)]
	if !ok {
		return RefreshTokenRecord{}, ErrRefreshTokenNotFound
	}
	return *rec, nil
}

func (f *fakeStore) RotateRefreshToken(_ context.Context, oldID uuid.UUID, replacement RefreshTokenRecord) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.refreshByID[oldID]
	if !ok || old.RevokedAt != nil {
		return false, nil
	}
	now := time.Now()
	old.RevokedAt = &now
	old.ReplacedBy = &replacement.ID
	f.putLocked(replacement)
	return true, nil
}

func (f *fakeStore) RevokeRefreshTokenFamily(_ context.Context, familyID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, rec := range f.refreshByID {
		if rec.FamilyID == familyID && rec.RevokedAt == nil {
			rec.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeStore) GetIdempotencyKey(_ context.Context, scope uuid.UUID, key string) (*IdempotencyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.idem[scope.String()+"|"+key], nil
}

func (f *fakeStore) InsertIdempotencyKey(_ context.Context, rec IdempotencyRecord) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := rec.Scope.String() + "|" + rec.Key
	if _, ok := f.idem[k]; ok {
		return false, nil
	}
	copyRec := rec
	f.idem[k] = &copyRec
	return true, nil
}

type fakeEnv struct {
	mu      sync.Mutex
	ensured map[string]bool
	phase   string
}

func (e *fakeEnv) Ensure(_ context.Context, userID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ensured == nil {
		e.ensured = map[string]bool{}
	}
	e.ensured[userID] = true
	return nil
}

func (e *fakeEnv) Phase(context.Context, string) (string, error) {
	if e.phase == "" {
		return "Pending", nil
	}
	return e.phase, nil
}

func newTestService(t *testing.T, store Store, env EnvProvisioner, now func() time.Time) *Service {
	t.Helper()
	p := DefaultArgon2Params()
	p.Memory = 8 * 1024
	p.Iterations = 1
	svc, err := NewService(Config{
		Store:           store,
		Issuer:          newTestIssuer(t),
		Env:             env,
		Argon2:          p,
		RefreshTokenTTL: 14 * 24 * time.Hour,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func fixedClock(ts time.Time) func() time.Time { return func() time.Time { return ts } }

func TestRegisterAndDuplicateEmail(t *testing.T) {
	store := newFakeStore()
	env := &fakeEnv{}
	svc := newTestService(t, store, env, fixedClock(time.Unix(1_700_000_000, 0)))
	ctx := context.Background()

	res, err := svc.Register(ctx, "User@Example.com", "correcthorse")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if res.User.Email != "user@example.com" {
		t.Fatalf("email not normalized: %q", res.User.Email)
	}
	if !env.ensured[res.User.ID.String()] {
		t.Fatal("environment was not ensured after register")
	}
	if res.EnvironmentPhase != "Pending" {
		t.Fatalf("phase = %q, want Pending", res.EnvironmentPhase)
	}

	if _, err := svc.Register(ctx, "user@example.com", "correcthorse"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate register error = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	svc := newTestService(t, newFakeStore(), &fakeEnv{}, fixedClock(time.Unix(1, 0)))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "not-an-email", "correcthorse"); !errors.Is(err, ErrValidation) {
		t.Fatalf("bad email error = %v, want ErrValidation", err)
	}
	if _, err := svc.Register(ctx, "a@b.co", "short"); !errors.Is(err, ErrValidation) {
		t.Fatalf("short password error = %v, want ErrValidation", err)
	}
}

func TestLoginSuccessAndFailures(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, &fakeEnv{}, fixedClock(time.Unix(1_700_000_000, 0)))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@b.co", "correcthorse"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	tok, err := svc.Login(ctx, "a@b.co", "correcthorse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatal("Login() returned empty tokens")
	}

	if _, err := svc.Login(ctx, "a@b.co", "wrongpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(ctx, "missing@b.co", "correcthorse"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginBootstrapsMissingAdminWithoutReplacingExistingPassword(t *testing.T) {
	store := newFakeStore()
	env := &fakeEnv{}
	svc := newTestService(t, store, env, fixedClock(time.Unix(1_700_000_000, 0)))
	ctx := context.Background()

	if _, err := svc.Login(ctx, " admin ", "short"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("short bootstrap password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := store.GetUserByEmail(ctx, bootstrapAdmin); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("admin created with invalid password, lookup error = %v", err)
	}

	tok, err := svc.Login(ctx, "Admin", "correcthorse")
	if err != nil {
		t.Fatalf("first admin Login() error = %v", err)
	}
	if tok.User.Email != bootstrapAdmin || tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatalf("first admin Login() returned incomplete result: %+v", tok)
	}
	if !env.ensured[tok.User.ID.String()] {
		t.Fatal("admin environment was not ensured during bootstrap")
	}

	if _, err := svc.Login(ctx, bootstrapAdmin, "wrongpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("existing admin wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(ctx, bootstrapAdmin, "correcthorse"); err != nil {
		t.Fatalf("existing admin password was replaced: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.usersByID) != 1 {
		t.Fatalf("admin user count = %d, want 1", len(store.usersByID))
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, &fakeEnv{}, fixedClock(time.Unix(1_700_000_000, 0)))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@b.co", "correcthorse"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	login, err := svc.Login(ctx, "a@b.co", "correcthorse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	rotated, err := svc.Refresh(ctx, login.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if rotated.RefreshToken == login.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	// 重用已轮换的旧 token：撤销整个 family。
	if _, err := svc.Refresh(ctx, login.RefreshToken); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("reuse error = %v, want ErrInvalidCredentials", err)
	}
	// family 已撤销，新 token 也随之失效。
	if _, err := svc.Refresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("post-reuse rotated token error = %v, want ErrInvalidCredentials", err)
	}
}

func TestRefreshExpired(t *testing.T) {
	store := newFakeStore()
	clock := time.Unix(1_700_000_000, 0)
	svc := newTestService(t, store, &fakeEnv{}, func() time.Time { return clock })
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@b.co", "correcthorse"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	login, err := svc.Login(ctx, "a@b.co", "correcthorse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	clock = clock.Add(15 * 24 * time.Hour) // 超过 14 天
	if _, err := svc.Refresh(ctx, login.RefreshToken); !errors.Is(err, ErrRefreshExpired) {
		t.Fatalf("expired refresh error = %v, want ErrRefreshExpired", err)
	}
}

func TestLogoutRevokesFamily(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, &fakeEnv{}, fixedClock(time.Unix(1_700_000_000, 0)))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@b.co", "correcthorse"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	login, err := svc.Login(ctx, "a@b.co", "correcthorse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if err := svc.Logout(ctx, login.RefreshToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := svc.Refresh(ctx, login.RefreshToken); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("refresh after logout error = %v, want ErrInvalidCredentials", err)
	}
	// logout 对未知 token 幂等返回成功。
	if err := svc.Logout(ctx, "unknown-token"); err != nil {
		t.Fatalf("Logout(unknown) error = %v, want nil", err)
	}
}

func TestConcurrentRefreshSingleWinner(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, &fakeEnv{}, fixedClock(time.Unix(1_700_000_000, 0)))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@b.co", "correcthorse"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	login, err := svc.Login(ctx, "a@b.co", "correcthorse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := svc.Refresh(ctx, login.RefreshToken); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if success != 1 {
		t.Fatalf("concurrent refresh winners = %d, want exactly 1", success)
	}
}

func TestMeReturnsUserAndPhase(t *testing.T) {
	store := newFakeStore()
	env := &fakeEnv{phase: "Ready"}
	svc := newTestService(t, store, env, fixedClock(time.Unix(1_700_000_000, 0)))
	ctx := context.Background()
	res, err := svc.Register(ctx, "a@b.co", "correcthorse")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	me, err := svc.Me(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if me.User.Email != "a@b.co" || me.EnvironmentPhase != "Ready" {
		t.Fatalf("Me() = %+v", me)
	}
}

func TestProgressiveRehash(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()

	// 用弱参数预置一个用户哈希，模拟历史存量。
	weak := DefaultArgon2Params()
	weak.Memory = 8 * 1024
	weak.Iterations = 1
	id, _ := uuid.NewV7()
	weakHash, _ := HashPassword("correcthorse", weak)
	if _, err := store.CreateUser(ctx, id, "a@b.co", weakHash); err != nil {
		t.Fatalf("seed user error = %v", err)
	}

	// service 使用更强参数：登录后应升级存储哈希。
	strong := DefaultArgon2Params()
	strong.Memory = 16 * 1024
	strong.Iterations = 2
	svc, err := NewService(Config{
		Store:           store,
		Issuer:          newTestIssuer(t),
		Env:             &fakeEnv{},
		Argon2:          strong,
		RefreshTokenTTL: time.Hour,
		Now:             fixedClock(time.Unix(1_700_000_000, 0)),
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err := svc.Login(ctx, "a@b.co", "correcthorse"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	updated, _ := store.GetUserByID(ctx, id)
	if NeedsRehash(updated.PasswordHash, strong) {
		t.Fatal("password hash was not upgraded after login")
	}
}

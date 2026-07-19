package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/ratelimit"
)

// memStore 是 app 层 HTTP 测试用的内存 Store，实现 auth.Store 全部方法。
type memStore struct {
	mu            sync.Mutex
	idemOpMu      sync.Mutex
	usersByID     map[uuid.UUID]auth.User
	emailToID     map[string]uuid.UUID
	refreshByHash map[string]*auth.RefreshTokenRecord
	refreshByID   map[uuid.UUID]*auth.RefreshTokenRecord
	idem          map[string]*auth.IdempotencyRecord
}

func newMemStore() *memStore {
	return &memStore{
		usersByID:     map[uuid.UUID]auth.User{},
		emailToID:     map[string]uuid.UUID{},
		refreshByHash: map[string]*auth.RefreshTokenRecord{},
		refreshByID:   map[uuid.UUID]*auth.RefreshTokenRecord{},
		idem:          map[string]*auth.IdempotencyRecord{},
	}
}

func (m *memStore) CreateUser(_ context.Context, id uuid.UUID, email, hash string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.emailToID[email]; ok {
		return auth.User{}, auth.ErrEmailTaken
	}
	u := auth.User{ID: id, Email: email, PasswordHash: hash, CreatedAt: time.Unix(0, 0).UTC()}
	m.usersByID[id] = u
	m.emailToID[email] = id
	return u, nil
}

func (m *memStore) GetUserByEmail(_ context.Context, email string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.emailToID[email]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return m.usersByID[id], nil
}

func (m *memStore) GetUserByID(_ context.Context, id uuid.UUID) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[id]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return u, nil
}

func (m *memStore) UpdatePasswordHash(_ context.Context, id uuid.UUID, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.usersByID[id]
	u.PasswordHash = hash
	m.usersByID[id] = u
	return nil
}

func (m *memStore) ListUserIDs(context.Context) ([]string, error) { return nil, nil }

func (m *memStore) InsertRefreshToken(_ context.Context, rec auth.RefreshTokenRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := rec
	m.refreshByHash[hex.EncodeToString(rec.TokenHash)] = &c
	m.refreshByID[rec.ID] = &c
	return nil
}

func (m *memStore) GetRefreshTokenByHash(_ context.Context, hash []byte) (auth.RefreshTokenRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.refreshByHash[hex.EncodeToString(hash)]
	if !ok {
		return auth.RefreshTokenRecord{}, auth.ErrRefreshTokenNotFound
	}
	return *rec, nil
}

func (m *memStore) RotateRefreshToken(_ context.Context, oldID uuid.UUID, replacement auth.RefreshTokenRecord) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.refreshByID[oldID]
	if !ok || old.RevokedAt != nil {
		return false, nil
	}
	now := time.Now()
	old.RevokedAt = &now
	c := replacement
	m.refreshByHash[hex.EncodeToString(replacement.TokenHash)] = &c
	m.refreshByID[replacement.ID] = &c
	return true, nil
}

func (m *memStore) RevokeRefreshTokenFamily(_ context.Context, familyID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, rec := range m.refreshByID {
		if rec.FamilyID == familyID && rec.RevokedAt == nil {
			rec.RevokedAt = &now
		}
	}
	return nil
}

func (m *memStore) WithIdempotencyLock(_ context.Context, _ uuid.UUID, _ string, fn func() error) error {
	m.idemOpMu.Lock()
	defer m.idemOpMu.Unlock()
	return fn()
}

func (m *memStore) GetIdempotencyKey(_ context.Context, scope uuid.UUID, key string) (*auth.IdempotencyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.idem[scope.String()+"|"+key]
	if rec != nil && !time.Now().Before(rec.ExpiresAt) {
		return nil, nil
	}
	return rec, nil
}

func (m *memStore) InsertIdempotencyKey(_ context.Context, rec auth.IdempotencyRecord) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := rec.Scope.String() + "|" + rec.Key
	if existing, ok := m.idem[k]; ok && time.Now().Before(existing.ExpiresAt) {
		return false, nil
	}
	c := rec
	m.idem[k] = &c
	return true, nil
}

type memEnv struct{ phase string }

func (e memEnv) Ensure(context.Context, string) error { return nil }
func (e memEnv) Phase(context.Context, string) (string, error) {
	if e.phase == "" {
		return "Pending", nil
	}
	return e.phase, nil
}

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

func newTestAPI(t *testing.T, ipLimit int) http.Handler {
	t.Helper()
	router, _ := newTestAPIWithStore(t, ipLimit)
	return router
}

func newTestAPIWithStore(t *testing.T, ipLimit int) (http.Handler, *memStore) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	issuer, err := auth.NewJWTIssuer("bosun", priv, 15*time.Minute)
	if err != nil {
		t.Fatalf("NewJWTIssuer() error = %v", err)
	}
	store := newMemStore()
	p := auth.DefaultArgon2Params()
	p.Memory = 8 * 1024
	p.Iterations = 1
	svc, err := auth.NewService(auth.Config{
		Store: store, Issuer: issuer, Env: memEnv{phase: "Ready"},
		Argon2: p, RefreshTokenTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return NewAPIRouter(APIDeps{
		Database:     okPinger{},
		Auth:         svc,
		JWT:          issuer,
		Store:        store,
		LoginByIP:    ratelimit.New(ipLimit, time.Minute, 1000),
		LoginByEmail: ratelimit.New(100, time.Minute, 1000),
		Cookie:       CookieConfig{Name: "bosun_refresh", Path: "/api/v1/auth", Secure: false, TTL: time.Hour},
	}), store
}

func doJSON(t *testing.T, router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestRegisterRequiresIdempotencyKey(t *testing.T) {
	router := newTestAPI(t, 100)
	rec := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", `{"email":"a@b.co","password":"correcthorse"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if code := decodeEnvelope(t, rec)["code"].(float64); int(code) != 10001 {
		t.Fatalf("code = %v, want 10001", code)
	}
}

func TestRegisterIdempotentReplayAndConflict(t *testing.T) {
	router := newTestAPI(t, 100)
	h := map[string]string{"Idempotency-Key": "key-123"}

	first := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", `{"email":"a@b.co","password":"correcthorse"}`, h)
	if first.Code != http.StatusOK {
		t.Fatalf("first register status = %d body=%s", first.Code, first.Body.String())
	}
	replay := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", `{"email":"a@b.co","password":"correcthorse"}`, h)
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body mismatch:\n%s\n%s", first.Body.String(), replay.Body.String())
	}
	conflict := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", `{"email":"other@b.co","password":"correcthorse"}`, h)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409", conflict.Code)
	}
	if code := decodeEnvelope(t, conflict)["code"].(float64); int(code) != 10002 {
		t.Fatalf("conflict code = %v, want 10002", code)
	}
}

func TestRegisterConcurrentSameKeySameRequestReplaysOneUser(t *testing.T) {
	router, store := newTestAPIWithStore(t, 100)
	const body = `{"email":"a@b.co","password":"correcthorse"}`
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)

	for range 2 {
		go func() {
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "concurrent-key")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			results <- rec
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d; want 200, 200", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("concurrent replay bodies differ:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.usersByID) != 1 {
		t.Fatalf("users created = %d, want 1", len(store.usersByID))
	}
}

func TestRegisterConcurrentSameKeyDifferentRequestConflictsWithoutSideEffect(t *testing.T) {
	router, store := newTestAPIWithStore(t, 100)
	bodies := []string{
		`{"email":"a@b.co","password":"correcthorse"}`,
		`{"email":"other@b.co","password":"correcthorse"}`,
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, len(bodies))

	for _, body := range bodies {
		go func() {
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "conflicting-key")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			results <- rec
		}()
	}
	close(start)
	statuses := map[int]int{}
	for range bodies {
		statuses[(<-results).Code]++
	}
	if statuses[http.StatusOK] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("status counts = %v, want one 200 and one 409", statuses)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.usersByID) != 1 {
		t.Fatalf("users created = %d, want 1", len(store.usersByID))
	}
}

func TestLoginMeRefreshLogoutFlow(t *testing.T) {
	router := newTestAPI(t, 100)
	reg := doJSON(t, router, http.MethodPost, "/api/v1/auth/register",
		`{"email":"a@b.co","password":"correcthorse"}`, map[string]string{"Idempotency-Key": "k1"})
	if reg.Code != http.StatusOK {
		t.Fatalf("register status = %d", reg.Code)
	}

	login := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", `{"email":"a@b.co","password":"correcthorse"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	data := decodeEnvelope(t, login)["data"].(map[string]any)
	access := data["accessToken"].(string)
	if access == "" {
		t.Fatal("login returned empty access token")
	}
	var refreshCookie *http.Cookie
	for _, ck := range login.Result().Cookies() {
		if ck.Name == "bosun_refresh" {
			refreshCookie = ck
		}
	}
	if refreshCookie == nil || !refreshCookie.HttpOnly {
		t.Fatal("login did not set HttpOnly refresh cookie")
	}

	// /me 需 Bearer；无 token → 401。
	noAuth := doJSON(t, router, http.MethodGet, "/api/v1/me", "", nil)
	if noAuth.Code != http.StatusUnauthorized {
		t.Fatalf("me without token status = %d, want 401", noAuth.Code)
	}
	me := doJSON(t, router, http.MethodGet, "/api/v1/me", "", map[string]string{"Authorization": "Bearer " + access})
	if me.Code != http.StatusOK {
		t.Fatalf("me status = %d body=%s", me.Code, me.Body.String())
	}
	meData := decodeEnvelope(t, me)["data"].(map[string]any)
	if meData["environmentPhase"].(string) != "Ready" {
		t.Fatalf("me environmentPhase = %v", meData["environmentPhase"])
	}

	// refresh 携带 cookie → 轮换。
	refreshReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	refreshReq.AddCookie(refreshCookie)
	refreshRec := httptest.NewRecorder()
	router.ServeHTTP(refreshRec, refreshReq)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", refreshRec.Code, refreshRec.Body.String())
	}

	// 旧 refresh cookie 已轮换，再用即触发重用检测 → 401。
	reuseReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	reuseReq.AddCookie(refreshCookie)
	reuseRec := httptest.NewRecorder()
	router.ServeHTTP(reuseRec, reuseReq)
	if reuseRec.Code != http.StatusUnauthorized {
		t.Fatalf("reused refresh status = %d, want 401", reuseRec.Code)
	}
}

func TestLoginRateLimited(t *testing.T) {
	router := newTestAPI(t, 2)
	doJSON(t, router, http.MethodPost, "/api/v1/auth/register",
		`{"email":"a@b.co","password":"correcthorse"}`, map[string]string{"Idempotency-Key": "k1"})

	body := `{"email":"a@b.co","password":"correcthorse"}`
	if r := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", body, nil); r.Code != http.StatusOK {
		t.Fatalf("attempt 1 status = %d", r.Code)
	}
	if r := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", body, nil); r.Code != http.StatusOK {
		t.Fatalf("attempt 2 status = %d", r.Code)
	}
	blocked := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", body, nil)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 3 status = %d, want 429", blocked.Code)
	}
	if code := decodeEnvelope(t, blocked)["code"].(float64); int(code) != 20003 {
		t.Fatalf("rate limited code = %v, want 20003", code)
	}
}

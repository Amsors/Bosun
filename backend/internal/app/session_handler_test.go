package app

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/ratelimit"
	"github.com/Amsors/Bosun/backend/internal/session"
)

func TestSessionRoutesCreateListTransitionsAndDestructiveDelete(t *testing.T) {
	fake := &fakeSessionService{}
	router, token, userID := newSessionTestAPI(t, fake)
	headers := map[string]string{
		"Authorization":   "Bearer " + token,
		"Idempotency-Key": "session-key",
	}
	create := doJSON(t, router, http.MethodPost, "/api/v1/sessions",
		`{"name":"课程项目","priority":"high","tier":"small","runtime":"claude-code","provider":{"mode":"platform"},"storagePolicy":"local"}`,
		headers,
	)
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	if fake.userID != userID || fake.createKey != "session-key" {
		t.Fatalf("create identity/key = %s/%q", fake.userID, fake.createKey)
	}

	list := doJSON(t, router, http.MethodGet, "/api/v1/sessions?page=2&page_size=25", "", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if list.Code != http.StatusOK || fake.page != 2 || fake.pageSize != 25 {
		t.Fatalf("list status=%d page=%d size=%d body=%s", list.Code, fake.page, fake.pageSize, list.Body.String())
	}

	for path, action := range map[string]session.Action{
		"/api/v1/sessions/" + fake.rec.ID.String() + "/hibernate": session.ActionHibernate,
		"/api/v1/sessions/" + fake.rec.ID.String() + "/resume":    session.ActionResume,
		"/api/v1/sessions/" + fake.rec.ID.String() + "/retry":     session.ActionRetry,
	} {
		response := doJSON(t, router, http.MethodPost, path, "", map[string]string{"Authorization": "Bearer " + token})
		if response.Code != http.StatusAccepted || fake.action != action {
			t.Fatalf("%s status=%d action=%s body=%s", path, response.Code, fake.action, response.Body.String())
		}
	}

	deleted := doJSON(t, router, http.MethodDelete, "/api/v1/sessions/"+fake.rec.ID.String(), "", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if deleted.Code != http.StatusAccepted || !strings.Contains(deleted.Body.String(), "永久删除") {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestSessionRoutesValidateIdempotencyPaginationAndOwnershipShape(t *testing.T) {
	fake := &fakeSessionService{}
	router, token, _ := newSessionTestAPI(t, fake)
	authHeader := map[string]string{"Authorization": "Bearer " + token}
	missingKey := doJSON(t, router, http.MethodPost, "/api/v1/sessions", `{}`, authHeader)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status=%d", missingKey.Code)
	}
	badPage := doJSON(t, router, http.MethodGet, "/api/v1/sessions?page_size=101", "", authHeader)
	if badPage.Code != http.StatusBadRequest {
		t.Fatalf("bad pagination status=%d", badPage.Code)
	}
	notUUID := doJSON(t, router, http.MethodGet, "/api/v1/sessions/not-a-uuid", "", authHeader)
	if notUUID.Code != http.StatusNotFound {
		t.Fatalf("invalid session ID status=%d", notUUID.Code)
	}
	unauthenticated := doJSON(t, router, http.MethodGet, "/api/v1/sessions", "", nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.Code)
	}
}

type fakeSessionService struct {
	rec       session.Session
	userID    uuid.UUID
	createKey string
	page      int32
	pageSize  int32
	action    session.Action
}

func (f *fakeSessionService) ensureRecord(userID uuid.UUID) session.Session {
	if f.rec.ID == uuid.Nil {
		id, _ := uuid.NewV7()
		nonce, _ := uuid.NewV7()
		f.rec = session.Session{
			ID: id, UserID: userID, Name: "课程项目", Priority: "high",
			Tier: "small", Runtime: "claude-code",
			Provider: session.Provider{Mode: "platform"}, StoragePolicy: "local",
			DesiredState: "Running", ResumeNonce: nonce, Phase: "Pending",
			Conditions: nil, CreatedAt: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
		}
	}
	return f.rec
}

func (f *fakeSessionService) Create(
	_ context.Context,
	userID uuid.UUID,
	key, _, _ string,
	_ []byte,
	_ session.CreateRequest,
) (session.CreateOutput, error) {
	f.userID = userID
	f.createKey = key
	rec := f.ensureRecord(userID)
	body, _ := json.Marshal(map[string]any{"code": 0, "message": "ok", "data": session.ToDTO(rec)})
	return session.CreateOutput{Status: http.StatusAccepted, Body: body}, nil
}

func (f *fakeSessionService) List(_ context.Context, userID uuid.UUID, page, pageSize int32) (session.Page, error) {
	f.userID, f.page, f.pageSize = userID, page, pageSize
	return session.Page{Items: []session.Session{f.ensureRecord(userID)}, Total: 1}, nil
}

func (f *fakeSessionService) Get(_ context.Context, userID, _ uuid.UUID) (session.Session, error) {
	return f.ensureRecord(userID), nil
}

func (f *fakeSessionService) Transition(
	_ context.Context,
	userID, _ uuid.UUID,
	action session.Action,
) (session.Session, error) {
	f.action = action
	return f.ensureRecord(userID), nil
}

func (f *fakeSessionService) Delete(_ context.Context, userID, _ uuid.UUID) (session.Session, error) {
	rec := f.ensureRecord(userID)
	rec.Phase = "Deleting"
	return rec, nil
}

func newSessionTestAPI(t *testing.T, sessions sessionService) (http.Handler, string, uuid.UUID) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error=%v", err)
	}
	issuer, err := auth.NewJWTIssuer("bosun", privateKey, 15*time.Minute)
	if err != nil {
		t.Fatalf("NewJWTIssuer() error=%v", err)
	}
	store := newMemStore()
	params := auth.DefaultArgon2Params()
	params.Memory = 8 * 1024
	params.Iterations = 1
	authService, err := auth.NewService(auth.Config{
		Store: store, Issuer: issuer, Env: memEnv{phase: "Ready"},
		Argon2: params, RefreshTokenTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("auth.NewService() error=%v", err)
	}
	userID, _ := uuid.NewV7()
	token, err := issuer.Sign(userID.String(), time.Now().UTC())
	if err != nil {
		t.Fatalf("Sign() error=%v", err)
	}
	router := NewAPIRouter(APIDeps{
		Database: okPinger{}, Auth: authService, JWT: issuer, Store: store,
		LoginByIP:    ratelimit.New(100, time.Minute, 1000),
		LoginByEmail: ratelimit.New(100, time.Minute, 1000),
		Cookie:       CookieConfig{Name: "bosun_refresh", Path: "/api/v1/auth", TTL: time.Hour},
		Sessions:     sessions,
	})
	return router, token, userID
}

package session

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Amsors/Bosun/backend/internal/database"
	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

// Set BOSUN_TEST_DATABASE_URL to run PostgreSQL migration/sqlc integration coverage.

func TestSessionStoreRepairAndOutOfOrderProjectionIntegration(t *testing.T) {
	pool := sessionIntegrationPool(t)
	ctx := context.Background()
	userID, _ := uuid.NewV7()
	if _, err := pool.Exec(ctx,
		"INSERT INTO bosun.users (id, email, password_hash) VALUES ($1, $2, $3)",
		userID, "session-integration@example.com", "hash",
	); err != nil {
		t.Fatalf("insert integration user: %v", err)
	}
	rec := testSession(userID)
	rec.CRNamespace = "bosun-u-integration"
	rec.CRName = "sess-integration"
	event, _ := newEvent(rec.ID, "session.created", map[string]any{}, rec.CreatedAt)
	store := NewPgxStore(pool)
	if err := store.CreateWithEventAndIdempotency(ctx, rec, event, IdempotencyInput{
		Key: "integration", Method: "POST", Path: "/api/v1/sessions", RequestHash: []byte("hash"),
		Status: 202, Body: []byte(`{"code":0}`), ExpiresAt: rec.CreatedAt.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("CreateWithEventAndIdempotency() error = %v", err)
	}

	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	control := NewCRControl(k8sClient)
	repairer := NewRepairer(store, control, time.Minute, nil)
	if err := repairer.RunOnce(ctx); err != nil {
		t.Fatalf("repair RunOnce() error = %v", err)
	}
	cr, err := control.Get(ctx, rec)
	if err != nil {
		t.Fatalf("repaired AgentSession Get() error = %v", err)
	}
	if cr.Spec.SessionID != rec.ID.String() {
		t.Fatalf("repaired spec.sessionID = %q", cr.Spec.SessionID)
	}

	newerEvent, _ := newEvent(rec.ID, "session.phase_changed", map[string]any{}, rec.CreatedAt.Add(time.Second))
	updated, err := store.Project(ctx, Projection{
		SessionID: rec.ID, Phase: "Running", PhaseReason: "SessionRunning",
		ResourceVersion: 10, OccurredAt: newerEvent.OccurredAt,
	}, newerEvent)
	if err != nil || !updated {
		t.Fatalf("newer Project() updated=%v error=%v", updated, err)
	}
	olderEvent, _ := newEvent(rec.ID, "session.phase_changed", map[string]any{}, rec.CreatedAt.Add(2*time.Second))
	updated, err = store.Project(ctx, Projection{
		SessionID: rec.ID, Phase: "Provisioning", PhaseReason: "Provisioning",
		ResourceVersion: 9, OccurredAt: olderEvent.OccurredAt,
	}, olderEvent)
	if err != nil || updated {
		t.Fatalf("older Project() updated=%v error=%v", updated, err)
	}
	got, err := store.Get(ctx, userID, rec.ID)
	if err != nil || got.Phase != "Running" {
		t.Fatalf("projected session phase=%q error=%v, want Running", got.Phase, err)
	}
}

func sessionIntegrationPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS bosun CASCADE; DROP TABLE IF EXISTS public.schema_migrations"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := database.Migrate(ctx, url, 10*time.Second); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return pool
}

package userenv

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeLister struct {
	ids []string
	err error
}

func (f fakeLister) ListUserIDs(context.Context) ([]string, error) { return f.ids, f.err }

type fakeProvisioner struct {
	existing map[string]struct{}
	ensured  []string
	ensesErr error
}

func (f *fakeProvisioner) Ensure(_ context.Context, userID string) error {
	if f.ensesErr != nil {
		return f.ensesErr
	}
	f.ensured = append(f.ensured, userID)
	if f.existing == nil {
		f.existing = map[string]struct{}{}
	}
	f.existing[userID] = struct{}{}
	return nil
}

func (f *fakeProvisioner) ExistingUserIDs(context.Context) (map[string]struct{}, error) {
	return f.existing, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunOnceRepairsOnlyMissing(t *testing.T) {
	lister := fakeLister{ids: []string{"u1", "u2", "u3"}}
	prov := &fakeProvisioner{existing: map[string]struct{}{"u2": {}}}
	r := NewRepairer(lister, prov, time.Minute, quietLogger())

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(prov.ensured) != 2 {
		t.Fatalf("ensured %v, want exactly u1 and u3", prov.ensured)
	}
	for _, id := range prov.ensured {
		if id == "u2" {
			t.Fatal("re-created environment for user that already had one")
		}
	}
}

func TestRunOncePropagatesListError(t *testing.T) {
	lister := fakeLister{err: errors.New("db down")}
	r := NewRepairer(lister, &fakeProvisioner{}, time.Minute, quietLogger())
	if err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() expected error when user listing fails")
	}
}

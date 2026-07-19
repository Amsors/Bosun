package userenv

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

func newFakeProvisioner(t *testing.T) *CRProvisioner {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	return NewCRProvisioner(c)
}

func TestEnsureCreatesAndIsIdempotent(t *testing.T) {
	p := newFakeProvisioner(t)
	ctx := context.Background()
	userID := "018f9c6e-1234-7000-8000-abcdef012345"

	if err := p.Ensure(ctx, userID); err != nil {
		t.Fatalf("Ensure() first call error = %v", err)
	}
	// 第二次调用必须幂等，不报 AlreadyExists。
	if err := p.Ensure(ctx, userID); err != nil {
		t.Fatalf("Ensure() second call error = %v", err)
	}

	ids, err := p.ExistingUserIDs(ctx)
	if err != nil {
		t.Fatalf("ExistingUserIDs() error = %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("environment count = %d, want 1", len(ids))
	}
	if _, ok := ids[userID]; !ok {
		t.Fatalf("expected environment for user %s", userID)
	}
}

func TestEnsureSetsSpecAndLabelsWithoutEmail(t *testing.T) {
	p := newFakeProvisioner(t)
	ctx := context.Background()
	userID := "018f9c6e-1234-7000-8000-abcdef012345"
	if err := p.Ensure(ctx, userID); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	var list bosunv1alpha1.UserEnvironmentList
	if err := p.client.List(ctx, &list); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("item count = %d, want 1", len(list.Items))
	}
	ue := list.Items[0]
	if ue.Name != CRName(userID) {
		t.Fatalf("CR name = %q, want %q", ue.Name, CRName(userID))
	}
	if ue.Spec.Namespace != Namespace(userID) {
		t.Fatalf("namespace = %q, want %q", ue.Spec.Namespace, Namespace(userID))
	}
	if ue.Spec.QuotaProfile != "mvp" {
		t.Fatalf("quotaProfile = %q, want mvp", ue.Spec.QuotaProfile)
	}
	if ue.Labels["app.kubernetes.io/managed-by"] != "bosun" || ue.Labels["bosun.io/user"] != userID {
		t.Fatalf("labels missing required management labels: %v", ue.Labels)
	}
}

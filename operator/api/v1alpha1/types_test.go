package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersFrozenKinds(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("register Bosun API types: %v", err)
	}

	tests := []struct {
		kind string
	}{
		{kind: "UserEnvironment"},
		{kind: "UserEnvironmentList"},
		{kind: "AgentSession"},
		{kind: "AgentSessionList"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()
			got, err := scheme.New(SchemeGroupVersion.WithKind(tt.kind))
			if err != nil {
				t.Fatalf("create %s from scheme: %v", tt.kind, err)
			}
			if _, ok := got.(interface{ DeepCopyObject() runtime.Object }); !ok {
				t.Fatalf("%s does not implement runtime.Object", tt.kind)
			}
		})
	}
}

func TestFrozenAPIGroup(t *testing.T) {
	t.Parallel()
	if got := SchemeGroupVersion.String(); got != "bosun.io/v1alpha1" {
		t.Fatalf("scheme group version = %q, want bosun.io/v1alpha1", got)
	}
}

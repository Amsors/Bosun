package monitor

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

func TestPodMetricFromUnstructuredParsesKubernetesQuantities(t *testing.T) {
	item := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "demo", "name": "agent"},
		"containers": []any{
			map[string]any{
				"name": "agent",
				"usage": map[string]any{
					"cpu": "125m", "memory": "256Mi",
				},
			},
		},
	}}
	result, err := podMetricFromUnstructured(item)
	if err != nil {
		t.Fatalf("podMetricFromUnstructured() error = %v", err)
	}
	usage := result.Containers["agent"]
	if usage.CPUMillicores != 125 || usage.MemoryBytes != 256*1024*1024 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestKubernetesSourcePersistsResourceScalingIntent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := agentSession("session-1")
	objects := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&session).Build()
	source := &KubernetesSource{objects: objects}

	updated, err := source.UpdateResourceScaling(
		context.Background(),
		session.Namespace,
		session.Name,
		session.Spec.SessionID,
		&bosunv1alpha1.ResourceScalingSpec{
			Mode: bosunv1alpha1.ResourceScalingModeManual,
			ManualLimits: &bosunv1alpha1.ResourceValues{
				CPUMillicores: 700,
				MemoryBytes:   1536 * 1024 * 1024,
			},
		},
	)
	if err != nil {
		t.Fatalf("UpdateResourceScaling() error = %v", err)
	}
	if updated.Spec.ResourceScaling == nil ||
		updated.Spec.ResourceScaling.Mode != bosunv1alpha1.ResourceScalingModeManual ||
		updated.Spec.ResourceScaling.ManualLimits.CPUMillicores != 700 {
		t.Fatalf("resourceScaling = %#v", updated.Spec.ResourceScaling)
	}
}

package monitor

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

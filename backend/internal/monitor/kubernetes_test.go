package monitor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
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

func TestKubernetesSourceUsesResizeSubresourceAndPreservesRequests(t *testing.T) {
	pod := agentPod()
	client := kubernetesfake.NewSimpleClientset(&pod)
	source := &KubernetesSource{core: client}

	updated, err := source.ResizePod(
		context.Background(),
		pod.Namespace,
		pod.Name,
		agentContainerName,
		Resources{CPUMillicores: 700, MemoryBytes: 1536 * 1024 * 1024},
	)
	if err != nil {
		t.Fatalf("ResizePod() error = %v", err)
	}
	agent := findContainer(updated, agentContainerName)
	if agent.Resources.Requests.Cpu().MilliValue() != 240 ||
		agent.Resources.Limits.Cpu().MilliValue() != 700 {
		t.Fatalf("agent resources = %#v", agent.Resources)
	}
	actions := client.Actions()
	last := actions[len(actions)-1]
	if last.GetVerb() != "update" || last.GetSubresource() != "resize" {
		t.Fatalf("last Kubernetes action = %s %q", last.GetVerb(), last.GetSubresource())
	}
	if findContainer(updated, "auth-proxy").Resources.Limits.Cpu().MilliValue() != 50 {
		t.Fatal("ResizePod changed the auth-proxy limits")
	}
	if _, ok := agent.Resources.Limits[corev1.ResourceMemory]; !ok {
		t.Fatal("ResizePod omitted the memory limit")
	}
}

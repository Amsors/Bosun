package monitor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPodMetricFromUnstructuredParsesKubernetesQuantities(t *testing.T) {
	item := &unstructured.Unstructured{Object: map[string]any{
		"metadata":  map[string]any{"namespace": "demo", "name": "agent"},
		"timestamp": "2026-07-26T06:00:00Z",
		"window":    "15s",
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
	if result.Source != "metrics-server" ||
		!result.ObservedAt.Equal(time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)) ||
		result.Window != 15*time.Second {
		t.Fatalf("metric metadata = %#v", result)
	}
}

func TestPodCountersFromSummaryParsesContainerCounters(t *testing.T) {
	raw := []byte(`{
		"pods": [{
			"podRef": {"namespace": "demo", "name": "agent-1"},
			"containers": [{
				"name": "agent",
				"cpu": {
					"time": "2026-07-26T06:00:00.25Z",
					"usageCoreNanoSeconds": 12250000000
				},
				"memory": {"workingSetBytes": 268435456}
			}]
		}]
	}`)
	result, err := podCountersFromSummary(raw)
	if err != nil {
		t.Fatalf("podCountersFromSummary() error = %v", err)
	}
	agent := result["demo/agent-1"].Containers["agent"]
	if agent.CPUUsageSeconds != 12.25 || agent.MemoryWorkingSetBytes != 256*1024*1024 {
		t.Fatalf("agent counter = %#v", agent)
	}
	expectedContainerObservedAt := time.Date(2026, 7, 26, 6, 0, 0, 250000000, time.UTC)
	if !agent.ObservedAt.Equal(expectedContainerObservedAt) {
		t.Fatalf("container observedAt = %v, want %v", agent.ObservedAt, expectedContainerObservedAt)
	}
	if !result["demo/agent-1"].ObservedAt.Equal(expectedContainerObservedAt) {
		t.Fatalf("observedAt = %v", result["demo/agent-1"].ObservedAt)
	}
}

func TestKubernetesSourceReadsStatsSummaryThroughNodeProxy(t *testing.T) {
	var requestedPath string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedPath = request.URL.Path
		body := `{"pods":[{"podRef":{"namespace":"demo","name":"agent-1"},"containers":[{
			"name":"agent",
			"cpu":{"time":"2026-07-26T06:00:00Z","usageCoreNanoSeconds":12250000000},
			"memory":{"workingSetBytes":268435456}
		}]}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}

	coreClient, err := kubernetes.NewForConfigAndClient(
		&rest.Config{Host: "https://kubernetes.example"},
		httpClient,
	)
	if err != nil {
		t.Fatalf("create Kubernetes client: %v", err)
	}
	source := &KubernetesSource{core: coreClient}
	result, err := source.GetNodePodCounters(context.Background(), "worker-1")
	if err != nil {
		t.Fatalf("GetNodePodCounters() error = %v", err)
	}
	if requestedPath != "/api/v1/nodes/worker-1/proxy/stats/summary" {
		t.Fatalf("requested path = %q", requestedPath)
	}
	if result["demo/agent-1"].Containers["agent"].CPUUsageSeconds != 12.25 {
		t.Fatalf("result = %#v", result)
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

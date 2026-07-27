package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type metricsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f metricsRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAgentCountersFromSummaryParsesOnlyAgentContainers(t *testing.T) {
	raw := []byte(`{
		"pods": [{
			"podRef": {"namespace": "demo", "name": "agent-1"},
			"containers": [
				{
					"name": "auth-proxy",
					"cpu": {
						"time": "2026-07-26T06:00:00Z",
						"usageCoreNanoSeconds": 999999999
					}
				},
				{
					"name": "agent",
					"cpu": {
						"time": "2026-07-26T06:00:00.25Z",
						"usageCoreNanoSeconds": 12250000000
					}
				}
			]
		}]
	}`)

	result, err := agentCountersFromSummary(raw)
	if err != nil {
		t.Fatalf("agentCountersFromSummary() error = %v", err)
	}
	counter := result["demo/agent-1"]
	if counter.usage != 12250000000 {
		t.Fatalf("usage = %d", counter.usage)
	}
	expected := time.Date(2026, 7, 26, 6, 0, 0, 250000000, time.UTC)
	if !counter.observedAt.Equal(expected) {
		t.Fatalf("observedAt = %v, want %v", counter.observedAt, expected)
	}
}

func TestKubeletPodMetricsReaderCalculatesCPUAndCachesNodeSummary(t *testing.T) {
	base := time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)
	requests := 0
	coreClient := kubeletTestClient(t, func(request *http.Request) string {
		requests++
		if request.URL.Path != "/api/v1/nodes/worker-1/proxy/stats/summary" {
			t.Fatalf("requested path = %q", request.URL.Path)
		}
		observedAt := base
		usage := uint64(10_000_000_000)
		if requests > 1 {
			observedAt = base.Add(2 * time.Second)
			usage += 800_000_000
		}
		return kubeletSummaryBody(observedAt, usage)
	})
	reader := NewKubeletPodMetricsReader(coreClient)
	pod := kubeletMetricTestPod("agent-1", "pod-1")
	secondPod := kubeletMetricTestPod("agent-2", "pod-2")

	reader.BeginCycle()
	if _, ready, err := reader.GetAgentPodMetric(context.Background(), pod); err != nil || ready {
		t.Fatalf("first observation = ready %t, error %v", ready, err)
	}
	if _, ready, err := reader.GetAgentPodMetric(
		context.Background(), secondPod,
	); err == nil || ready {
		t.Fatalf("missing Pod observation = ready %t, error %v", ready, err)
	}
	if requests != 1 {
		t.Fatalf("Summary requests in one cycle = %d, want 1", requests)
	}

	reader.BeginCycle()
	metric, ready, err := reader.GetAgentPodMetric(context.Background(), pod)
	if err != nil || !ready {
		t.Fatalf("second observation = ready %t, error %v", ready, err)
	}
	if metric.CPUUsageMillicores != 400 ||
		metric.Window != 2*time.Second ||
		!metric.Timestamp.Equal(base.Add(2*time.Second)) {
		t.Fatalf("metric = %#v", metric)
	}

	reader.BeginCycle()
	_, ready, err = reader.GetAgentPodMetric(context.Background(), pod)
	if err != nil || ready {
		t.Fatalf("duplicate observation = ready %t, error %v", ready, err)
	}
}

func TestAgentMetricIdentityChangesAfterContainerRestart(t *testing.T) {
	pod := kubeletMetricTestPod("agent-1", "pod-1")
	before := agentMetricIdentity(pod)
	pod.Status.ContainerStatuses[0].RestartCount++
	pod.Status.ContainerStatuses[0].ContainerID = "containerd://replacement"
	after := agentMetricIdentity(pod)
	if before == after {
		t.Fatalf("identity did not change after restart: %q", before)
	}
}

func TestKubeletPodMetricsReaderCachesNodeFailureWithinCycle(t *testing.T) {
	requests := 0
	coreClient := kubeletClientForHTTP(t, &http.Client{Transport: metricsRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("Node proxy unavailable")
		},
	)})
	reader := NewKubeletPodMetricsReader(coreClient)
	reader.BeginCycle()

	for _, pod := range []*corev1.Pod{
		kubeletMetricTestPod("agent-1", "pod-1"),
		kubeletMetricTestPod("agent-2", "pod-2"),
	} {
		if _, ready, err := reader.GetAgentPodMetric(
			context.Background(), pod,
		); err == nil || ready {
			t.Fatalf("failed observation = ready %t, error %v", ready, err)
		}
	}
	if requests != 1 {
		t.Fatalf("failed Summary requests in one cycle = %d, want 1", requests)
	}
}

func kubeletTestClient(
	t *testing.T,
	body func(*http.Request) string,
) kubernetes.Interface {
	t.Helper()
	httpClient := &http.Client{Transport: metricsRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body(request))),
				Request:    request,
			}, nil
		},
	)}
	return kubeletClientForHTTP(t, httpClient)
}

func kubeletClientForHTTP(
	t *testing.T,
	httpClient *http.Client,
) kubernetes.Interface {
	t.Helper()
	client, err := kubernetes.NewForConfigAndClient(
		&rest.Config{Host: "https://kubernetes.example"},
		httpClient,
	)
	if err != nil {
		t.Fatalf("create Kubernetes client: %v", err)
	}
	return client
}

func kubeletMetricTestPod(name, uid string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "demo",
			Name:      name,
			UID:       types.UID(uid),
		},
		Spec: corev1.PodSpec{NodeName: "worker-1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:        agentContainerName,
			ContainerID: "containerd://" + uid,
		}}},
	}
}

func kubeletSummaryBody(observedAt time.Time, usage uint64) string {
	return fmt.Sprintf(`{
		"pods": [{
			"podRef": {"namespace": "demo", "name": "agent-1"},
			"containers": [{
				"name": "agent",
				"cpu": {
					"time": %q,
					"usageCoreNanoSeconds": %d
				}
			}]
		}]
	}`, observedAt.Format(time.RFC3339Nano), usage)
}

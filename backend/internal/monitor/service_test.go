package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Amsors/Bosun/backend/internal/session"
)

func TestClusterAggregatesPodResourcesMetricsAndAgentOwner(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	source := &fakeSource{
		nodes: []corev1.Node{{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Labels: map[string]string{"role": "worker"}},
			Status: corev1.NodeStatus{
				Capacity:    resources("4", "8Gi"),
				Allocatable: resources("3900m", "7Gi"),
				NodeInfo:    corev1.NodeSystemInfo{KubeletVersion: "v1.36.0"},
				Conditions: []corev1.NodeCondition{{
					Type: corev1.NodeReady, Status: corev1.ConditionTrue,
				}},
			},
		}},
		pods: []corev1.Pod{agentPod()},
		podMetrics: map[string]PodMetric{
			"bosun-u-1/agent-session-1": {Containers: map[string]Resources{
				"agent":      {CPUMillicores: 125, MemoryBytes: 256 * 1024 * 1024},
				"auth-proxy": {CPUMillicores: 5, MemoryBytes: 12 * 1024 * 1024},
			}},
		},
		nodeMetrics: map[string]Resources{
			"worker-1": {CPUMillicores: 500, MemoryBytes: 2 * 1024 * 1024 * 1024},
		},
	}
	service, err := NewService(
		fakeSessionStore{},
		fakeOwners{"session-1": {Username: "student@example.com", SessionName: "课程演示"}},
		source,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	service.now = func() time.Time { return now }

	result, err := service.Cluster(context.Background())
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	if !result.PodMetricsAvailable || !result.NodeMetricsAvailable || result.ObservedAt != now {
		t.Fatalf("availability/time = %#v", result)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Status != "Ready" ||
		result.Nodes[0].Usage == nil || result.Nodes[0].Usage.CPUMillicores != 500 {
		t.Fatalf("node snapshot = %#v", result.Nodes)
	}
	if len(result.Pods) != 1 {
		t.Fatalf("pods = %#v", result.Pods)
	}
	pod := result.Pods[0]
	if !pod.IsAgent || pod.Username != "student@example.com" || pod.SessionName != "课程演示" {
		t.Fatalf("agent identity = %#v", pod)
	}
	if pod.Usage == nil || pod.Usage.CPUMillicores != 130 ||
		pod.Usage.MemoryBytes != 268*1024*1024 {
		t.Fatalf("pod usage = %#v", pod.Usage)
	}
	if pod.Limits.CPUMillicores != 500 || pod.Limits.MemoryBytes != 1024*1024*1024 {
		t.Fatalf("pod limits = %#v", pod.Limits)
	}
}

func TestSessionAllowsResourceSpecWhenMetricsAreUnavailable(t *testing.T) {
	userID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012401")
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	pod := agentPod()
	pod.Name = "agent-" + sessionID.String()
	source := &fakeSource{
		pod: pod,
		podMetricErr: apierrors.NewNotFound(
			schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"},
			pod.Name,
		),
	}
	service, err := NewService(
		fakeSessionStore{record: session.Session{
			ID: sessionID, UserID: userID, CRNamespace: pod.Namespace,
		}},
		fakeOwners{},
		source,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	result, err := service.Session(context.Background(), userID, sessionID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if result.MetricsAvailable || result.Pod.Usage != nil {
		t.Fatalf("unexpected metrics = %#v", result)
	}
	if result.Pod.Limits.CPUMillicores != 500 {
		t.Fatalf("limits = %#v", result.Pod.Limits)
	}
}

func TestResizeAgentUsesPodResizeAndReturnsUpdatedLimits(t *testing.T) {
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	pod := agentPod()
	pod.Labels["bosun.io/session"] = sessionID.String()
	source := &fakeSource{
		pods:         []corev1.Pod{pod},
		podMetricErr: errors.New("metrics decode failed after successful resize"),
	}
	service, err := NewService(fakeSessionStore{}, fakeOwners{}, source)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	result, err := service.ResizeAgent(context.Background(), sessionID, ResizeRequest{
		CPUMillicores: 700,
		MemoryBytes:   1536 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("ResizeAgent() error = %v", err)
	}
	if source.resizeContainer != agentContainerName {
		t.Fatalf("resize container = %q", source.resizeContainer)
	}
	agent := findContainer(&source.resizedPod, agentContainerName)
	if agent == nil ||
		agent.Resources.Limits.Cpu().MilliValue() != 700 ||
		agent.Resources.Limits.Memory().Value() != 1536*1024*1024 {
		t.Fatalf("resized agent = %#v", agent)
	}
	if result.Pod.Limits.CPUMillicores != 750 ||
		result.Pod.Limits.MemoryBytes != 1600*1024*1024 {
		t.Fatalf("aggregate limits = %#v", result.Pod.Limits)
	}
	if result.MetricsAvailable {
		t.Fatal("resize response unexpectedly reported unavailable metrics as available")
	}
}

func TestResizeAgentRejectsLimitsBelowRequests(t *testing.T) {
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	pod := agentPod()
	pod.Labels["bosun.io/session"] = sessionID.String()
	source := &fakeSource{pods: []corev1.Pod{pod}}
	service, err := NewService(fakeSessionStore{}, fakeOwners{}, source)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.ResizeAgent(context.Background(), sessionID, ResizeRequest{
		CPUMillicores: 200,
		MemoryBytes:   1024 * 1024 * 1024,
	})
	if !errors.Is(err, ErrInvalidResize) {
		t.Fatalf("ResizeAgent() error = %v, want ErrInvalidResize", err)
	}
	if source.resizeContainer != "" {
		t.Fatal("ResizePod was called for an invalid request")
	}
}

type fakeSessionStore struct {
	record session.Session
	err    error
}

func (f fakeSessionStore) Get(context.Context, uuid.UUID, uuid.UUID) (session.Session, error) {
	return f.record, f.err
}

type fakeOwners map[string]AgentOwner

func (f fakeOwners) ListAgentOwners(context.Context) (map[string]AgentOwner, error) {
	return f, nil
}

type fakeSource struct {
	pod             corev1.Pod
	pods            []corev1.Pod
	nodes           []corev1.Node
	podMetric       PodMetric
	podMetricErr    error
	podMetrics      map[string]PodMetric
	podMetricsErr   error
	nodeMetrics     map[string]Resources
	nodeMetricErr   error
	resizeContainer string
	resizedPod      corev1.Pod
}

func (f *fakeSource) GetPod(context.Context, string, string) (*corev1.Pod, error) {
	return f.pod.DeepCopy(), nil
}

func (f *fakeSource) ListPods(context.Context) ([]corev1.Pod, error) {
	return f.pods, nil
}

func (f *fakeSource) ResizePod(
	_ context.Context,
	namespace, name, containerName string,
	limits Resources,
) (*corev1.Pod, error) {
	f.resizeContainer = containerName
	for i := range f.pods {
		if f.pods[i].Namespace != namespace || f.pods[i].Name != name {
			continue
		}
		f.resizedPod = *f.pods[i].DeepCopy()
		container := findContainer(&f.resizedPod, containerName)
		container.Resources.Limits[corev1.ResourceCPU] =
			*resource.NewMilliQuantity(limits.CPUMillicores, resource.DecimalSI)
		container.Resources.Limits[corev1.ResourceMemory] =
			*resource.NewQuantity(limits.MemoryBytes, resource.BinarySI)
		return f.resizedPod.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, name)
}

func (f *fakeSource) ListNodes(context.Context) ([]corev1.Node, error) {
	return f.nodes, nil
}

func (f *fakeSource) GetPodMetric(context.Context, string, string) (PodMetric, error) {
	return f.podMetric, f.podMetricErr
}

func (f *fakeSource) ListPodMetrics(context.Context) (map[string]PodMetric, error) {
	return f.podMetrics, f.podMetricsErr
}

func (f *fakeSource) ListNodeMetrics(context.Context) (map[string]Resources, error) {
	return f.nodeMetrics, f.nodeMetricErr
}

func agentPod() corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "bosun-u-1",
			Name:      "agent-session-1",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "bosun",
				"bosun.io/session":             "session-1",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-1",
			Containers: []corev1.Container{
				{
					Name: "agent",
					Resources: corev1.ResourceRequirements{
						Requests: resources("240m", "496Mi"),
						Limits:   resources("450m", "960Mi"),
					},
				},
				{
					Name: "auth-proxy",
					Resources: corev1.ResourceRequirements{
						Requests: resources("10m", "16Mi"),
						Limits:   resources("50m", "64Mi"),
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
			}},
		},
	}
}

func resources(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}
}

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
	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
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
			"bosun-u-1/agent-session-1": {
				ObservedAt: now.Add(-15 * time.Second),
				Window:     15 * time.Second,
				Source:     "metrics-server",
				Containers: map[string]Resources{
					"agent":      {CPUMillicores: 125, MemoryBytes: 256 * 1024 * 1024},
					"auth-proxy": {CPUMillicores: 5, MemoryBytes: 12 * 1024 * 1024},
				},
			},
		},
		nodeMetrics: map[string]Resources{
			"worker-1": {CPUMillicores: 500, MemoryBytes: 2 * 1024 * 1024 * 1024},
		},
		agentSessions: []bosunv1alpha1.AgentSession{agentSession("session-1")},
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
	if pod.MetricsObservedAt == nil || !pod.MetricsObservedAt.Equal(now.Add(-15*time.Second)) ||
		pod.MetricsWindowSeconds != 15 || pod.MetricsSource != "metrics-server" {
		t.Fatalf("pod metric metadata = %#v", pod)
	}
	if pod.Limits.CPUMillicores != 500 || pod.Limits.MemoryBytes != 1024*1024*1024 {
		t.Fatalf("pod limits = %#v", pod.Limits)
	}
	if pod.ResourceScaling == nil || !pod.ResourceScaling.ActualResourcesAvailable ||
		pod.ResourceScaling.ActualResources == nil ||
		pod.ResourceScaling.ActualResources.CPUMillicores != 440 {
		t.Fatalf("agent actual resources = %#v", pod.ResourceScaling)
	}
}

func TestClusterPrefersLiveKubeletMetricsAfterCPUCounterWarmup(t *testing.T) {
	start := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	source := &fakeSource{
		nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}},
		pods:  []corev1.Pod{agentPod()},
		podMetrics: map[string]PodMetric{
			"bosun-u-1/agent-session-1": {
				ObservedAt: start.Add(-15 * time.Second),
				Source:     "metrics-server",
				Containers: map[string]Resources{
					"agent":      {CPUMillicores: 100, MemoryBytes: 200},
					"auth-proxy": {CPUMillicores: 5, MemoryBytes: 20},
				},
			},
		},
		nodeMetrics:   map[string]Resources{},
		agentSessions: []bosunv1alpha1.AgentSession{agentSession("session-1")},
		nodePodCounters: map[string]PodCounterMetric{
			"bosun-u-1/agent-session-1": {
				ObservedAt: start,
				Containers: map[string]ContainerCounter{
					"agent":      {CPUUsageSeconds: 10, MemoryWorkingSetBytes: 300},
					"auth-proxy": {CPUUsageSeconds: 1, MemoryWorkingSetBytes: 30},
				},
			},
		},
	}
	service, err := NewService(fakeSessionStore{}, fakeOwners{}, source)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	first, err := service.Cluster(context.Background())
	if err != nil {
		t.Fatalf("first Cluster() error = %v", err)
	}
	if first.Pods[0].MetricsSource != "metrics-server" {
		t.Fatalf("first metrics source = %q", first.Pods[0].MetricsSource)
	}

	source.nodePodCounters = map[string]PodCounterMetric{
		"bosun-u-1/agent-session-1": {
			ObservedAt: start.Add(time.Second),
			Containers: map[string]ContainerCounter{
				"agent":      {CPUUsageSeconds: 10.25, MemoryWorkingSetBytes: 320},
				"auth-proxy": {CPUUsageSeconds: 1.01, MemoryWorkingSetBytes: 31},
			},
		},
	}
	second, err := service.Cluster(context.Background())
	if err != nil {
		t.Fatalf("second Cluster() error = %v", err)
	}
	pod := second.Pods[0]
	if pod.MetricsSource != "kubelet-summary" || pod.MetricsObservedAt == nil ||
		!pod.MetricsObservedAt.Equal(start.Add(time.Second)) {
		t.Fatalf("second metric metadata = %#v", pod)
	}
	if pod.Usage == nil || pod.Usage.CPUMillicores != 260 || pod.Usage.MemoryBytes != 351 {
		t.Fatalf("second usage = %#v", pod.Usage)
	}
}

func TestSessionAllowsResourceSpecWhenMetricsAreUnavailable(t *testing.T) {
	userID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012401")
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	pod := agentPod()
	pod.Name = "agent-" + sessionID.String()
	source := &fakeSource{
		pod: pod,
		agentSessions: []bosunv1alpha1.AgentSession{
			agentSession(sessionID.String()),
		},
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

func TestResizeAgentPersistsManualIntentWithoutCallingPodResize(t *testing.T) {
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	pod := agentPod()
	pod.Name = "agent-" + sessionID.String()
	pod.Labels["bosun.io/session"] = sessionID.String()
	source := &fakeSource{
		pod:           pod,
		pods:          []corev1.Pod{pod},
		agentSessions: []bosunv1alpha1.AgentSession{agentSession(sessionID.String())},
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
	if source.updatedScaling == nil ||
		source.updatedScaling.Mode != bosunv1alpha1.ResourceScalingModeManual ||
		source.updatedScaling.ManualLimits == nil {
		t.Fatalf("updated scaling = %#v", source.updatedScaling)
	}
	if source.updatedScaling.ManualLimits.CPUMillicores != 700 ||
		source.updatedScaling.ManualLimits.MemoryBytes != 1536*1024*1024 {
		t.Fatalf("manual limits = %#v", source.updatedScaling.ManualLimits)
	}
	if result.Mode != string(bosunv1alpha1.ResourceScalingModeManual) ||
		result.DesiredResources.CPUMillicores != 450 {
		t.Fatalf("response = %#v", result)
	}
}

func TestResizeAgentRejectsLimitsOutsideTierBounds(t *testing.T) {
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	pod := agentPod()
	pod.Labels["bosun.io/session"] = sessionID.String()
	source := &fakeSource{
		pods:          []corev1.Pod{pod},
		agentSessions: []bosunv1alpha1.AgentSession{agentSession(sessionID.String())},
	}
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
	if source.updatedScaling != nil {
		t.Fatal("resource scaling was updated for an invalid request")
	}
}

func TestRestoreAutoClearsManualIntent(t *testing.T) {
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	pod := agentPod()
	pod.Name = "agent-" + sessionID.String()
	cr := agentSession(sessionID.String())
	cr.Spec.ResourceScaling = &bosunv1alpha1.ResourceScalingSpec{
		Mode: bosunv1alpha1.ResourceScalingModeManual,
		ManualLimits: &bosunv1alpha1.ResourceValues{
			CPUMillicores: 700,
			MemoryBytes:   1024 * 1024 * 1024,
		},
	}
	source := &fakeSource{pod: pod, agentSessions: []bosunv1alpha1.AgentSession{cr}}
	service, err := NewService(fakeSessionStore{}, fakeOwners{}, source)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	result, err := service.RestoreAuto(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("RestoreAuto() error = %v", err)
	}
	if source.updatedScaling == nil ||
		source.updatedScaling.Mode != bosunv1alpha1.ResourceScalingModeAuto ||
		source.updatedScaling.ManualLimits != nil {
		t.Fatalf("updated scaling = %#v", source.updatedScaling)
	}
	if result.Mode != string(bosunv1alpha1.ResourceScalingModeAuto) || result.ManualLimits != nil {
		t.Fatalf("response = %#v", result)
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
	pod               corev1.Pod
	pods              []corev1.Pod
	nodes             []corev1.Node
	podMetric         PodMetric
	podMetricErr      error
	podMetrics        map[string]PodMetric
	podMetricsErr     error
	nodeMetrics       map[string]Resources
	nodeMetricErr     error
	nodePodCounters   map[string]PodCounterMetric
	nodePodCounterErr error
	agentSessions     []bosunv1alpha1.AgentSession
	updatedScaling    *bosunv1alpha1.ResourceScalingSpec
}

func (f *fakeSource) GetPod(context.Context, string, string) (*corev1.Pod, error) {
	return f.pod.DeepCopy(), nil
}

func (f *fakeSource) ListPods(context.Context) ([]corev1.Pod, error) {
	return f.pods, nil
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

func (f *fakeSource) GetNodePodCounters(
	context.Context,
	string,
) (map[string]PodCounterMetric, error) {
	return f.nodePodCounters, f.nodePodCounterErr
}

func (f *fakeSource) GetAgentSession(
	_ context.Context,
	_, _ string,
) (*bosunv1alpha1.AgentSession, error) {
	if len(f.agentSessions) == 0 {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "bosun.io", Resource: "agentsessions"}, "")
	}
	return f.agentSessions[0].DeepCopy(), nil
}

func (f *fakeSource) ListAgentSessions(context.Context) ([]bosunv1alpha1.AgentSession, error) {
	return f.agentSessions, nil
}

func (f *fakeSource) UpdateResourceScaling(
	_ context.Context,
	_, _ string,
	expectedSessionID string,
	scaling *bosunv1alpha1.ResourceScalingSpec,
) (*bosunv1alpha1.AgentSession, error) {
	for i := range f.agentSessions {
		if f.agentSessions[i].Spec.SessionID != expectedSessionID {
			continue
		}
		f.updatedScaling = scaling.DeepCopy()
		f.agentSessions[i].Spec.ResourceScaling = scaling.DeepCopy()
		return f.agentSessions[i].DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(
		schema.GroupResource{Group: "bosun.io", Resource: "agentsessions"},
		expectedSessionID,
	)
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
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:      "agent",
					Resources: &corev1.ResourceRequirements{Limits: resources("440m", "950Mi")},
				},
				{
					Name:      "auth-proxy",
					Resources: &corev1.ResourceRequirements{Limits: resources("50m", "64Mi")},
				},
			},
		},
	}
}

func resources(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}
}

func agentSession(sessionID string) bosunv1alpha1.AgentSession {
	return bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "bosun-u-1",
			Name:      "agent-" + sessionID,
		},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID: sessionID,
			Tier:      bosunv1alpha1.SessionTierSmall,
			ResourceScaling: &bosunv1alpha1.ResourceScalingSpec{
				Mode: bosunv1alpha1.ResourceScalingModeAuto,
			},
		},
	}
}

package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

const testWorkerNodeName = "worker-1"

type fakePodResizer struct {
	pod   *corev1.Pod
	calls int
	err   error
}

func (f *fakePodResizer) UpdateResize(_ context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	f.calls++
	f.pod = pod.DeepCopy()
	if f.err != nil {
		return nil, f.err
	}
	return f.pod.DeepCopy(), nil
}

type fakePodMetricsReader struct {
	metrics []AgentPodMetric
	err     error
	calls   int
}

func (*fakePodMetricsReader) BeginCycle() {}

func (f *fakePodMetricsReader) GetAgentPodMetric(
	_ context.Context,
	_ *corev1.Pod,
) (AgentPodMetric, bool, error) {
	f.calls++
	if f.err != nil {
		return AgentPodMetric{}, false, f.err
	}
	if len(f.metrics) == 0 {
		return AgentPodMetric{}, false, nil
	}
	index := min(f.calls-1, len(f.metrics)-1)
	return f.metrics[index], true, nil
}

func TestResourceAutoscalerDoublesCPUAndSynchronizesRequestAndLimit(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	session, k8s := autoScalingFixture(t, normalPriorityClass)
	metrics := highMetrics(now)
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{
		Client: k8s, Resizer: resizer, Metrics: metrics,
		Now: func() time.Time { return now },
	}
	for range 3 {
		if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}
	if resizer.calls != 1 {
		t.Fatalf("resize calls = %d, want 1", resizer.calls)
	}
	agent := findPodContainer(resizer.pod, agentContainerName)
	if agent.Resources.Limits.Cpu().MilliValue() != 1000 ||
		agent.Resources.Requests.Cpu().MilliValue() != 1000 {
		t.Fatalf("agent resources = %#v, want request=limit=1000m", agent.Resources)
	}
	if agent.Resources.Requests.Memory().Value() != 2*1024*1024*1024 ||
		agent.Resources.Limits.Memory().Value() != 2*1024*1024*1024 {
		t.Fatalf("agent memory changed = %#v", agent.Resources)
	}
	if findPodContainer(resizer.pod, "auth-proxy").Resources.Limits.Cpu().MilliValue() != 50 {
		t.Fatal("auth-proxy resources changed")
	}
}

func TestLowPriorityNeverScalesAboveBase(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	session, k8s := autoScalingFixture(t, lowPriorityClass)
	metrics := highMetrics(now)
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{
		Client: k8s, Resizer: resizer, Metrics: metrics,
		Now: func() time.Time { return now },
	}
	for range 3 {
		if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}
	if resizer.calls != 0 {
		t.Fatalf("low priority resize calls = %d, want 0", resizer.calls)
	}
	if metrics.calls != 0 {
		t.Fatalf("low priority metric calls = %d, want 0", metrics.calls)
	}
}

func TestPlanNodeFairlyFillsDemandCaps(t *testing.T) {
	states := []*autoscaleSession{
		{key: "a", priority: 2, current: 500, target: 500, demand: 700, scaleUp: true},
		{key: "b", priority: 2, current: 500, target: 500, demand: 1300, scaleUp: true},
	}
	(&ResourceAutoscaler{}).planNode(states, 800)
	if states[0].target != 700 || states[1].target != 1100 {
		t.Fatalf("targets = %dm, %dm; want 700m, 1100m", states[0].target, states[1].target)
	}
}

func TestPlanNodeReclaimsNormalCPUProportionallyForHighPriority(t *testing.T) {
	states := []*autoscaleSession{
		{key: "normal-a", priority: 2, current: 700, target: 700, demand: 700},
		{key: "normal-b", priority: 2, current: 1300, target: 1300, demand: 1300},
		{key: "high", priority: 3, current: 500, target: 500, demand: 1000, scaleUp: true},
	}
	(&ResourceAutoscaler{}).planNode(states, 0)
	targets := map[string]int64{}
	for _, state := range states {
		targets[state.key] = state.target
	}
	if targets["normal-a"] != 600 || targets["normal-b"] != 900 || targets["high"] != 1000 {
		t.Fatalf(
			"targets = %dm, %dm, %dm; want 600m, 900m, 1000m",
			targets["normal-a"], targets["normal-b"], targets["high"],
		)
	}
}

func TestPlanNodeFairlySharesHighPriorityCapacity(t *testing.T) {
	states := []*autoscaleSession{
		{key: "high-a", priority: 3, current: 500, target: 500, demand: 2000, scaleUp: true},
		{key: "high-b", priority: 3, current: 500, target: 500, demand: 2000, scaleUp: true},
		{key: "normal", priority: 2, current: 1000, target: 1000, demand: 1000},
	}
	(&ResourceAutoscaler{}).planNode(states, 100)
	targets := map[string]int64{}
	for _, state := range states {
		targets[state.key] = state.target
		if state.target < agentBaseCPUMillicores {
			t.Fatalf("%s target = %dm, below base quota", state.key, state.target)
		}
	}
	if targets["high-a"] != 800 || targets["high-b"] != 800 ||
		targets["normal"] != 500 {
		t.Fatalf("targets = %#v, want both high=800m and normal=500m", targets)
	}
}

func TestNodeFreeCPUUsesAllocatableAndAllContainerLimits(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testWorkerNodeName}}
	node.Status.Allocatable = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod"},
		Spec: corev1.PodSpec{
			NodeName: testWorkerNodeName,
			InitContainers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("25m"),
				}},
			}},
			Containers: []corev1.Container{
				{Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"),
				}}},
				{Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("50m"),
				}}},
			},
		},
	}
	if got := nodeFreeCPU(node, []corev1.Pod{pod}); got != 1425 {
		t.Fatalf("nodeFreeCPU() = %dm, want 1425m", got)
	}
}

func TestNodeFreeCPUIgnoresTerminalPods(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testWorkerNodeName}}
	node.Status.Allocatable = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
	}
	terminalPod := func(name string, phase corev1.PodPhase) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PodSpec{
				NodeName: testWorkerNodeName,
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("32"),
					}},
				}},
			},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	pods := []corev1.Pod{
		terminalPod("succeeded", corev1.PodSucceeded),
		terminalPod("failed", corev1.PodFailed),
	}
	if got := nodeFreeCPU(node, pods); got != 2000 {
		t.Fatalf("nodeFreeCPU() = %dm, want 2000m", got)
	}
}

func TestNodeFreeMemoryUsesEffectivePodRequests(t *testing.T) {
	node := readyWorkerNode(testWorkerNodeName, "2")
	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			NodeName: testWorkerNodeName,
			Containers: []corev1.Container{
				{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				}}},
				{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("16Mi"),
				}}},
			},
			InitContainers: []corev1.Container{
				{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("3Gi"),
				}}},
			},
		},
	}
	want := resource.MustParse("5Gi")
	if got := nodeFreeMemory(&node, []corev1.Pod{pod}); got != want.Value() {
		t.Fatalf("nodeFreeMemory() = %d, want %d", got, want.Value())
	}
}

func TestPendingReservationUsesPriorityOrderAndStableNodeChoice(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	low := waitingSession("low", lowPriorityClass, time.Unix(1, 0))
	high := waitingSession("high", highPriorityClass, time.Unix(2, 0))
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(low, high).Build()
	nodes := []corev1.Node{
		readyWorkerNode("worker-b", "2"),
		readyWorkerNode("worker-a", "2"),
	}
	autoscaler := &ResourceAutoscaler{Client: k8s, Resizer: &fakePodResizer{}}
	autoscaler.reserveWaitingSession(
		context.Background(),
		[]bosunv1alpha1.AgentSession{*low, *high},
		nil,
		nodes,
		nil,
	)

	var currentHigh bosunv1alpha1.AgentSession
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(high), &currentHigh); err != nil {
		t.Fatal(err)
	}
	if currentHigh.Annotations[schedulingNodeAnnotation] != "worker-a" {
		t.Fatalf(
			"high priority reservation = %q, want stable worker-a",
			currentHigh.Annotations[schedulingNodeAnnotation],
		)
	}
	var currentLow bosunv1alpha1.AgentSession
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(low), &currentLow); err != nil {
		t.Fatal(err)
	}
	if currentLow.Annotations[schedulingNodeAnnotation] != "" {
		t.Fatal("lower priority session was reserved before the high priority session")
	}
}

func TestPendingReservationPreservesExistingBaseWhenCapacityIsInsufficient(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := waitingSession("normal", normalPriorityClass, time.Unix(1, 0))
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	autoscaler := &ResourceAutoscaler{Client: k8s, Resizer: &fakePodResizer{}}
	autoscaler.reserveWaitingSession(
		context.Background(),
		[]bosunv1alpha1.AgentSession{*session},
		nil,
		[]corev1.Node{readyWorkerNode("worker-a", "400m")},
		nil,
	)
	var current bosunv1alpha1.AgentSession
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(session), &current); err != nil {
		t.Fatal(err)
	}
	if current.Annotations[schedulingNodeAnnotation] != "" {
		t.Fatal("session received a reservation without a full 500m base allocation")
	}
}

func TestPendingReservationUsesMemoryRequestToChooseEligibleNode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := waitingSession("large", normalPriorityClass, time.Unix(1, 0))
	session.Spec.MemoryRequest = resource.MustParse("6Gi")
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
	small := readyWorkerNode("worker-a-small", "2")
	small.Status.Allocatable[corev1.ResourceMemory] = resource.MustParse("4Gi")
	large := readyWorkerNode("worker-z-large", "2")
	large.Status.Allocatable[corev1.ResourceMemory] = resource.MustParse("8Gi")
	autoscaler := &ResourceAutoscaler{Client: k8s, Resizer: &fakePodResizer{}}
	autoscaler.reserveWaitingSession(
		context.Background(),
		[]bosunv1alpha1.AgentSession{*session},
		nil,
		[]corev1.Node{small, large},
		nil,
	)
	var current bosunv1alpha1.AgentSession
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(session), &current); err != nil {
		t.Fatal(err)
	}
	if got := current.Annotations[schedulingNodeAnnotation]; got != "worker-z-large" {
		t.Fatalf("memory-aware reservation = %q, want worker-z-large", got)
	}
}

func TestPendingReservationIsExcludedFromAutoscalingFreeCPU(t *testing.T) {
	session := waitingSession("reserved", normalPriorityClass, time.Unix(1, 0))
	session.Annotations = map[string]string{schedulingNodeAnnotation: testWorkerNodeName}
	reserved := pendingReservationCPU(
		[]bosunv1alpha1.AgentSession{*session},
		nil,
	)
	if reserved[testWorkerNodeName] != agentBaseCPUMillicores {
		t.Fatalf(
			"reserved CPU = %dm, want %dm",
			reserved[testWorkerNodeName], agentBaseCPUMillicores,
		)
	}
	reservedMemory := pendingReservationMemory(
		[]bosunv1alpha1.AgentSession{*session},
		nil,
	)
	wantMemory := int64(2*1024*1024*1024) + authProxyMemoryRequestBytes
	if reservedMemory[testWorkerNodeName] != wantMemory {
		t.Fatalf(
			"reserved memory = %d, want %d",
			reservedMemory[testWorkerNodeName], wantMemory,
		)
	}
}

func waitingSession(
	name string,
	priority string,
	createdAt time.Time,
) *bosunv1alpha1.AgentSession {
	return &bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "bosun-user", UID: types.UID(name),
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID: name, DesiredState: bosunv1alpha1.DesiredStateRunning,
			PriorityClassName: priority, MemoryRequest: resource.MustParse("2Gi"),
		},
		Status: bosunv1alpha1.AgentSessionStatus{
			Phase: bosunv1alpha1.AgentSessionPhasePending,
		},
	}
}

func readyWorkerNode(name, cpu string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Labels: map[string]string{
				agentNodeRoleLabel: agentNodeRoleValue,
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
			Conditions: []corev1.NodeCondition{{
				Type: corev1.NodeReady, Status: corev1.ConditionTrue,
			}},
		},
	}
}

func resourceScalingSession(priority string) *bosunv1alpha1.AgentSession {
	return &bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agent-018f9c6e-1234-7000-8000-abcdef012501", Namespace: "bosun-user",
			UID: "session-uid", Generation: 1,
		},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID:         "018f9c6e-1234-7000-8000-abcdef012501",
			DesiredState:      bosunv1alpha1.DesiredStateRunning,
			PriorityClassName: priority,
			MemoryRequest:     resource.MustParse("2Gi"),
		},
		Status: bosunv1alpha1.AgentSessionStatus{
			Phase: bosunv1alpha1.AgentSessionPhaseRunning,
			Conditions: []metav1.Condition{{
				Type: sessionReadyCondition, Status: metav1.ConditionTrue,
				Reason: awaitingInputReason,
			}},
		},
	}
}

func resourceScalingPod(session *bosunv1alpha1.AgentSession) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: sessionidentity.PodName(session.Spec.SessionID), Namespace: session.Namespace,
			UID:    "pod-uid",
			Labels: map[string]string{sessionLabel: session.Spec.SessionID},
		},
		Spec: corev1.PodSpec{NodeName: testWorkerNodeName, Containers: []corev1.Container{
			{
				Name: agentContainerName,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			},
			{
				Name: "auth-proxy",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
			},
		}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: agentContainerName,
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			}},
		},
	}
}

func autoScalingFixture(
	t *testing.T,
	priority string,
) (*bosunv1alpha1.AgentSession, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := resourceScalingSession(priority)
	pod := resourceScalingPod(session)
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(session, pod).
		WithObjects(session, pod).
		Build()
	return session, k8s
}

func highMetrics(now time.Time) *fakePodMetricsReader {
	return &fakePodMetricsReader{metrics: []AgentPodMetric{
		{Timestamp: now.Add(-30 * time.Second), CPUUsageMillicores: 400},
		{Timestamp: now.Add(-15 * time.Second), CPUUsageMillicores: 100},
		{Timestamp: now, CPUUsageMillicores: 400},
	}}
}

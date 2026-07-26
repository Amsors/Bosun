package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/resourcepolicy"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

type fakePodResizer struct {
	pod   *corev1.Pod
	calls int
	err   error
}

type manualBeforeResizeClient struct {
	client.Client
}

type fakePodMetricsReader struct {
	metrics []AgentPodMetric
	err     error
	calls   int
}

func (c *manualBeforeResizeClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	object client.Object,
	opts ...client.GetOption,
) error {
	if err := c.Client.Get(ctx, key, object, opts...); err != nil {
		return err
	}
	if session, ok := object.(*bosunv1alpha1.AgentSession); ok {
		session.Spec.ResourceScaling = &bosunv1alpha1.ResourceScalingSpec{
			Mode: bosunv1alpha1.ResourceScalingModeManual,
			ManualLimits: &bosunv1alpha1.ResourceValues{
				CPUMillicores: 700,
				MemoryBytes:   3 * 1024 * 1024 * 1024,
			},
		}
	}
	return nil
}

func (f *fakePodResizer) UpdateResize(_ context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	f.calls++
	f.pod = pod.DeepCopy()
	if f.err != nil {
		return nil, f.err
	}
	return f.pod.DeepCopy(), nil
}

func (f *fakePodMetricsReader) GetAgentPodMetric(
	_ context.Context,
	_, _ string,
) (AgentPodMetric, error) {
	f.calls++
	if f.err != nil {
		return AgentPodMetric{}, f.err
	}
	index := f.calls - 1
	if index >= len(f.metrics) {
		index = len(f.metrics) - 1
	}
	return f.metrics[index], nil
}

func TestResourceAutoscalerAppliesManualLimitsWithoutChangingRequestsOrSidecar(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := resourceScalingSession()
	session.Spec.ResourceScaling = &bosunv1alpha1.ResourceScalingSpec{
		Mode: bosunv1alpha1.ResourceScalingModeManual,
		ManualLimits: &bosunv1alpha1.ResourceValues{
			CPUMillicores: 700,
			MemoryBytes:   3 * 1024 * 1024 * 1024,
		},
	}
	session.Status.Phase = bosunv1alpha1.AgentSessionPhaseRunning
	session.Status.Conditions = []metav1.Condition{{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: awaitingInputReason,
	}}
	pod := resourceScalingPod(session)
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(session).
		WithObjects(session, pod).
		Build()
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{
		Client: k8s, Resizer: resizer,
		Now: func() time.Time { return time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC) },
	}

	if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
		t.Fatalf("reconcileSession() error = %v", err)
	}
	if resizer.calls != 1 {
		t.Fatalf("resize calls = %d", resizer.calls)
	}
	agent := findPodContainer(resizer.pod, agentContainerName)
	if agent.Resources.Limits.Cpu().MilliValue() != 700 ||
		agent.Resources.Limits.Memory().Value() != 3*1024*1024*1024 {
		t.Fatalf("agent limits = %#v", agent.Resources.Limits)
	}
	if agent.Resources.Requests.Cpu().MilliValue() != 500 {
		t.Fatalf("agent requests changed = %#v", agent.Resources.Requests)
	}
	if findPodContainer(resizer.pod, "auth-proxy").Resources.Limits.Cpu().MilliValue() != 50 {
		t.Fatal("auth-proxy resources changed")
	}
	var current bosunv1alpha1.AgentSession
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(session), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != bosunv1alpha1.AgentSessionPhaseRunning ||
		len(current.Status.Conditions) != 1 ||
		current.Status.Conditions[0].Reason != awaitingInputReason {
		t.Fatalf("existing status was not preserved = %#v", current.Status)
	}
}

func TestResourceAutoscalerRestoresAutoMemoryAndPreservesDesiredCPU(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = bosunv1alpha1.AddToScheme(scheme)
	session := resourceScalingSession()
	session.Spec.ResourceScaling = &bosunv1alpha1.ResourceScalingSpec{
		Mode: bosunv1alpha1.ResourceScalingModeAuto,
	}
	pod := resourceScalingPod(session)
	agent := findPodContainer(pod, agentContainerName)
	agent.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("800m")
	agent.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("2Gi")
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(session).
		WithObjects(session, pod).
		Build()
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{Client: k8s, Resizer: resizer}

	if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
		t.Fatalf("reconcileSession() error = %v", err)
	}
	updated := findPodContainer(resizer.pod, agentContainerName)
	if updated.Resources.Limits.Cpu().MilliValue() != 800 {
		t.Fatalf("CPU limit = %s, want 800m", updated.Resources.Limits.Cpu())
	}
	if updated.Resources.Limits.Memory().Value() != 3*1024*1024*1024 {
		t.Fatalf("memory limit = %s, want 3Gi", updated.Resources.Limits.Memory())
	}
}

func TestDesiredPodUsesPersistedManualLimits(t *testing.T) {
	session := resourceScalingSession()
	session.Spec.ResourceScaling = &bosunv1alpha1.ResourceScalingSpec{
		Mode: bosunv1alpha1.ResourceScalingModeManual,
		ManualLimits: &bosunv1alpha1.ResourceValues{
			CPUMillicores: 700,
			MemoryBytes:   3 * 1024 * 1024 * 1024,
		},
	}
	reconciler := &AgentSessionReconciler{}

	pod := reconciler.desiredPod(session, "workspace")
	agent := findPodContainer(pod, agentContainerName)
	if agent.Resources.Limits.Cpu().MilliValue() != 700 ||
		agent.Resources.Limits.Memory().Value() != 3*1024*1024*1024 {
		t.Fatalf("agent limits = %#v", agent.Resources.Limits)
	}
}

func TestResourceAutoscalerDoesNotApplyStaleAutoResultAfterManualSwitch(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = bosunv1alpha1.AddToScheme(scheme)
	session := resourceScalingSession()
	session.Spec.ResourceScaling = &bosunv1alpha1.ResourceScalingSpec{
		Mode: bosunv1alpha1.ResourceScalingModeAuto,
	}
	pod := resourceScalingPod(session)
	findPodContainer(pod, agentContainerName).Resources.Limits[corev1.ResourceMemory] = resource.MustParse("2Gi")
	base := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(session).
		WithObjects(session, pod).
		Build()
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{
		Client:  &manualBeforeResizeClient{Client: base},
		Resizer: resizer,
	}

	if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
		t.Fatalf("reconcileSession() error = %v", err)
	}
	if resizer.calls != 0 {
		t.Fatal("stale Auto result called pods/resize after mode switched to Manual")
	}
}

func TestResourceAutoscalerScalesUpOnlyAgentCPULimit(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	session, pod, k8s := autoScalingFixture(t)
	metrics := &fakePodMetricsReader{metrics: []AgentPodMetric{
		{Timestamp: now.Add(-30 * time.Second), CPUUsageMillicores: 400},
		{Timestamp: now.Add(-15 * time.Second), CPUUsageMillicores: 100},
		{Timestamp: now, CPUUsageMillicores: 400},
	}}
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{
		Client: k8s, Resizer: resizer, Metrics: metrics,
		Now: func() time.Time { return now },
	}

	for range 3 {
		if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
			t.Fatalf("reconcileSession() error = %v", err)
		}
	}
	if resizer.calls != 1 {
		t.Fatalf("resize calls = %d, want 1", resizer.calls)
	}
	agent := findPodContainer(resizer.pod, agentContainerName)
	if agent.Resources.Limits.Cpu().MilliValue() != 750 {
		t.Fatalf("CPU limit = %s, want 750m", agent.Resources.Limits.Cpu())
	}
	if agent.Resources.Limits.Memory().Value() != 3*1024*1024*1024 ||
		agent.Resources.Requests.Cpu().MilliValue() != 500 ||
		agent.Resources.Requests.Memory().Value() != 2*1024*1024*1024 {
		t.Fatalf("non-CPU Agent resources changed = %#v", agent.Resources)
	}
	sidecar := findPodContainer(resizer.pod, "auth-proxy")
	if sidecar.Resources.Limits.Cpu().MilliValue() != 50 {
		t.Fatal("auth-proxy resources changed")
	}
	var current bosunv1alpha1.AgentSession
	if err := k8s.Get(
		context.Background(), client.ObjectKeyFromObject(session), &current,
	); err != nil {
		t.Fatal(err)
	}
	if current.Status.ResourceScaling == nil ||
		current.Status.ResourceScaling.LoadClass != bosunv1alpha1.ResourceLoadClassCPUHigh ||
		current.Status.ResourceScaling.RecommendedCPUMillicores != 750 {
		t.Fatalf("resource scaling status = %#v", current.Status.ResourceScaling)
	}
	if len(autoscaler.window(session.UID).Samples()) != 0 {
		t.Fatal("successful resize did not clear the CPU sample window")
	}
	_ = pod
}

func TestResourceAutoscalerDoesNotScaleWhilePodResizeIsActive(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	session, pod, k8s := autoScalingFixture(t)
	pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
		Type: corev1.PodResizeInProgress, Status: corev1.ConditionTrue,
	})
	if err := k8s.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("active Pod resize did not suppress Auto resize")
	}
}

func TestResourceAutoscalerKeepsLimitWhenMetricsOrActualResourcesAreUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		removeActual bool
		metrics      PodMetricsReader
	}{
		{
			name:    "metrics unavailable",
			metrics: &fakePodMetricsReader{err: errors.New("metrics API unavailable")},
		},
		{
			name: "actual resources unavailable", removeActual: true,
			metrics: highMetrics(now),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, pod, k8s := autoScalingFixture(t)
			if tt.removeActual {
				pod.Status.ContainerStatuses = nil
				if err := k8s.Status().Update(context.Background(), pod); err != nil {
					t.Fatal(err)
				}
			}
			resizer := &fakePodResizer{}
			autoscaler := &ResourceAutoscaler{
				Client: k8s, Resizer: resizer, Metrics: tt.metrics,
				Now: func() time.Time { return now },
			}
			if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			if resizer.calls != 0 {
				t.Fatal("unavailable observation changed resources")
			}
			var current bosunv1alpha1.AgentSession
			if err := k8s.Get(
				context.Background(), client.ObjectKeyFromObject(session), &current,
			); err != nil {
				t.Fatal(err)
			}
			if current.Status.ResourceScaling == nil ||
				current.Status.ResourceScaling.LoadClass != bosunv1alpha1.ResourceLoadClassUnknown ||
				current.Status.ResourceScaling.LastError == "" {
				t.Fatalf("resource scaling status = %#v", current.Status.ResourceScaling)
			}
		})
	}
}

func TestResourceAutoscalerRequiresSafeWorkStateForScaleDown(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	session, pod, k8s := autoScalingFixture(t)
	setFixtureCPU(t, k8s, pod, "1000m")
	session.Status.Conditions[0].Reason = agentWorkingReason
	if err := k8s.Status().Update(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	metrics := lowMetrics(now)
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
		t.Fatal("AgentWorking session was scaled down")
	}
	var current bosunv1alpha1.AgentSession
	if err := k8s.Get(
		context.Background(), client.ObjectKeyFromObject(session), &current,
	); err != nil {
		t.Fatal(err)
	}
	if current.Status.ResourceScaling.LoadClass != bosunv1alpha1.ResourceLoadClassCPULow ||
		current.Status.ResourceScaling.RecommendedCPUMillicores != 500 {
		t.Fatalf("resource scaling status = %#v", current.Status.ResourceScaling)
	}
}

func TestResourceAutoscalerScalesDownAfterThreeLowSamplesWhileAwaitingInput(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	session, pod, k8s := autoScalingFixture(t)
	setFixtureCPU(t, k8s, pod, "1000m")
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{
		Client: k8s, Resizer: resizer, Metrics: lowMetrics(now),
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
	if agent.Resources.Limits.Cpu().MilliValue() != 500 {
		t.Fatalf("CPU limit = %s, want 500m", agent.Resources.Limits.Cpu())
	}
}

func TestResourceAutoscalerScalesDownGenericRunningSession(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	session, pod, k8s := autoScalingFixture(t)
	setFixtureCPU(t, k8s, pod, "1000m")
	session.Status.Conditions[0].Reason = "SessionRunning"
	if err := k8s.Status().Update(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	resizer := &fakePodResizer{}
	autoscaler := &ResourceAutoscaler{
		Client: k8s, Resizer: resizer, Metrics: lowMetrics(now),
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
}

func TestResourceAutoscalerRejectsStaleMetricsAndRewarmsAfterPodUIDChange(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	t.Run("stale metrics", func(t *testing.T) {
		session, _, k8s := autoScalingFixture(t)
		resizer := &fakePodResizer{}
		autoscaler := &ResourceAutoscaler{
			Client: k8s, Resizer: resizer,
			Metrics: &fakePodMetricsReader{metrics: []AgentPodMetric{{
				Timestamp:          now.Add(-resourcepolicy.MetricsMaxAge - time.Second),
				CPUUsageMillicores: 400,
			}}},
			Now: func() time.Time { return now },
		}
		if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		if resizer.calls != 0 {
			t.Fatal("stale metrics changed resources")
		}
		var current bosunv1alpha1.AgentSession
		if err := k8s.Get(
			context.Background(), client.ObjectKeyFromObject(session), &current,
		); err != nil {
			t.Fatal(err)
		}
		if current.Status.ResourceScaling.LoadClass != bosunv1alpha1.ResourceLoadClassUnknown {
			t.Fatalf("load class = %s", current.Status.ResourceScaling.LoadClass)
		}
	})

	t.Run("Pod UID change", func(t *testing.T) {
		session, pod, k8s := autoScalingFixture(t)
		metrics := &fakePodMetricsReader{metrics: []AgentPodMetric{
			{Timestamp: now.Add(-20 * time.Second), CPUUsageMillicores: 400},
			{Timestamp: now.Add(-10 * time.Second), CPUUsageMillicores: 400},
			{Timestamp: now, CPUUsageMillicores: 400},
		}}
		resizer := &fakePodResizer{}
		autoscaler := &ResourceAutoscaler{
			Client: k8s, Resizer: resizer, Metrics: metrics,
			Now: func() time.Time { return now },
		}
		for range 2 {
			if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
				t.Fatal(err)
			}
		}
		var replacement corev1.Pod
		if err := k8s.Get(
			context.Background(), client.ObjectKeyFromObject(pod), &replacement,
		); err != nil {
			t.Fatal(err)
		}
		replacement.UID = "replacement-pod-uid"
		if err := k8s.Update(context.Background(), &replacement); err != nil {
			t.Fatal(err)
		}
		if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		if resizer.calls != 0 {
			t.Fatal("replacement Pod reused old samples")
		}
		var current bosunv1alpha1.AgentSession
		if err := k8s.Get(
			context.Background(), client.ObjectKeyFromObject(session), &current,
		); err != nil {
			t.Fatal(err)
		}
		if current.Status.ResourceScaling.LoadClass != bosunv1alpha1.ResourceLoadClassWarmingUp {
			t.Fatalf(
				"load class = %s, want WarmingUp",
				current.Status.ResourceScaling.LoadClass,
			)
		}
	})
}

func TestResourceAutoscalerCooldownAndFailedIntentRetry(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	t.Run("cooldown", func(t *testing.T) {
		session, _, k8s := autoScalingFixture(t)
		applied := metav1.NewTime(now.Add(-30 * time.Second))
		session.Status.ResourceScaling = &bosunv1alpha1.ResourceScalingStatus{
			LastAppliedAt: &applied,
		}
		if err := k8s.Status().Update(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		resizer := &fakePodResizer{}
		autoscaler := &ResourceAutoscaler{
			Client: k8s, Resizer: resizer, Metrics: highMetrics(now),
			ScaleUpCooldown: time.Minute,
			Now:             func() time.Time { return now },
		}
		for range 3 {
			if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
				t.Fatal(err)
			}
		}
		if resizer.calls != 0 {
			t.Fatal("scale-up cooldown did not suppress resize")
		}
	})

	t.Run("failed intent retry", func(t *testing.T) {
		session, _, k8s := autoScalingFixture(t)
		resizer := &fakePodResizer{err: errors.New("resize rejected")}
		metrics := highMetrics(now)
		autoscaler := &ResourceAutoscaler{
			Client: k8s, Resizer: resizer, Metrics: metrics,
			Now: func() time.Time { return now },
		}
		for i := range 3 {
			err := autoscaler.reconcileSession(context.Background(), session)
			if i < 2 && err != nil {
				t.Fatal(err)
			}
			if i == 2 && err == nil {
				t.Fatal("failed resize did not return an error")
			}
		}
		if err := autoscaler.reconcileSession(context.Background(), session); err != nil {
			t.Fatalf("suppressed retry returned error = %v", err)
		}
		if resizer.calls != 1 {
			t.Fatalf("resize calls = %d, want one failed attempt", resizer.calls)
		}
	})
}

func resourceScalingSession() *bosunv1alpha1.AgentSession {
	return &bosunv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agent-018f9c6e-1234-7000-8000-abcdef012501", Namespace: "bosun-user",
			UID: "session-uid", Generation: 1,
		},
		Spec: bosunv1alpha1.AgentSessionSpec{
			SessionID:    "018f9c6e-1234-7000-8000-abcdef012501",
			DesiredState: bosunv1alpha1.DesiredStateRunning,
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
			UID: "pod-uid",
		},
		Spec: corev1.PodSpec{NodeName: "worker-1", Containers: []corev1.Container{
			{
				Name: agentContainerName,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("3Gi"),
					},
				},
			},
			{
				Name: "auth-proxy",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi"),
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
						corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("3Gi"),
					},
				},
			}},
		},
	}
}

func autoScalingFixture(
	t *testing.T,
) (*bosunv1alpha1.AgentSession, *corev1.Pod, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := resourceScalingSession()
	session.Spec.ResourceScaling = &bosunv1alpha1.ResourceScalingSpec{
		Mode: bosunv1alpha1.ResourceScalingModeAuto,
	}
	pod := resourceScalingPod(session)
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(session, pod).
		WithObjects(session, pod).
		Build()
	return session, pod, k8s
}

func setFixtureCPU(t *testing.T, k8s client.Client, pod *corev1.Pod, cpu string) {
	t.Helper()
	var current corev1.Pod
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(pod), &current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse(cpu)
	if err := k8s.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	current.Status.ContainerStatuses[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse(cpu)
	if err := k8s.Status().Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
}

func highMetrics(now time.Time) *fakePodMetricsReader {
	return &fakePodMetricsReader{metrics: []AgentPodMetric{
		{Timestamp: now.Add(-30 * time.Second), CPUUsageMillicores: 400},
		{Timestamp: now.Add(-15 * time.Second), CPUUsageMillicores: 100},
		{Timestamp: now, CPUUsageMillicores: 400},
	}}
}

func lowMetrics(now time.Time) *fakePodMetricsReader {
	metrics := make([]AgentPodMetric, 8)
	for i := range metrics {
		metrics[i] = AgentPodMetric{
			Timestamp:          now.Add(time.Duration(i-7) * 5 * time.Second),
			CPUUsageMillicores: 100,
		}
	}
	return &fakePodMetricsReader{metrics: metrics}
}

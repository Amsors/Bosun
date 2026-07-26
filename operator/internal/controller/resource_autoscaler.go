package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/resourcepolicy"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

const (
	defaultResourceSampleInterval = 15 * time.Second
	defaultResizeRetryInterval    = time.Minute
)

// PodResizer applies desired resources through the Pod resize subresource.
type PodResizer interface {
	UpdateResize(context.Context, *corev1.Pod) (*corev1.Pod, error)
}

type kubernetesPodResizer struct {
	client kubernetes.Interface
}

// NewPodResizer creates the production pods/resize client.
func NewPodResizer(coreClient kubernetes.Interface) PodResizer {
	return &kubernetesPodResizer{client: coreClient}
}

func (r *kubernetesPodResizer) UpdateResize(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	return r.client.CoreV1().Pods(pod.Namespace).UpdateResize(
		ctx, pod.Name, pod, metav1.UpdateOptions{},
	)
}

type failedResizeIntent struct {
	PodUID      types.UID
	Mode        bosunv1alpha1.ResourceScalingMode
	CPU         int64
	Memory      int64
	AttemptedAt time.Time
}

// ResourceAutoscaler owns all Agent Pod resize writes and the in-memory CPU
// sampling windows used by Auto mode.
type ResourceAutoscaler struct {
	Client            client.Client
	Resizer           PodResizer
	Metrics           PodMetricsReader
	SampleInterval    time.Duration
	ScaleUpCooldown   time.Duration
	ScaleDownCooldown time.Duration
	RetryInterval     time.Duration
	Now               func() time.Time

	windows        map[types.UID]*resourcepolicy.SampleWindow
	failedAttempts map[types.UID]failedResizeIntent
}

// +kubebuilder:rbac:groups=bosun.io,resources=agentsessions,verbs=get;list;watch
// +kubebuilder:rbac:groups=bosun.io,resources=agentsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/resize,verbs=update
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list

// Start runs one reconciliation immediately and then at the configured interval.
func (r *ResourceAutoscaler) Start(ctx context.Context) error {
	if r.Client == nil || r.Resizer == nil || r.Metrics == nil {
		return fmt.Errorf(
			"resource autoscaler requires Kubernetes client, Pod resizer, and PodMetrics reader",
		)
	}
	interval := r.SampleInterval
	if interval <= 0 {
		interval = defaultResourceSampleInterval
	}
	r.runOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *ResourceAutoscaler) runOnce(ctx context.Context) {
	var sessions bosunv1alpha1.AgentSessionList
	if err := r.Client.List(ctx, &sessions); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list AgentSessions for resource scaling")
		return
	}
	seen := make(map[types.UID]struct{}, len(sessions.Items))
	for i := range sessions.Items {
		session := &sessions.Items[i]
		if !session.DeletionTimestamp.IsZero() {
			continue
		}
		seen[session.UID] = struct{}{}
		if err := r.reconcileSession(ctx, session); err != nil {
			logf.FromContext(ctx).Error(
				err, "Could not reconcile AgentSession resources",
				"namespace", session.Namespace, "name", session.Name,
			)
		}
	}
	r.pruneState(seen)
}

func (r *ResourceAutoscaler) reconcileSession(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) error {
	scaling := session.Spec.EffectiveResourceScaling()
	if scaling.Mode == bosunv1alpha1.ResourceScalingModeManual {
		r.clearWindow(session.UID)
		if err := r.updateScalingStatus(
			ctx,
			client.ObjectKeyFromObject(session),
			func(status *bosunv1alpha1.ResourceScalingStatus) {
				status.LoadClass = ""
				status.RecommendedCPUMillicores = 0
			},
		); err != nil {
			return err
		}
	}

	podKey := types.NamespacedName{
		Namespace: session.Namespace,
		Name:      sessionidentity.PodName(session.Spec.SessionID),
	}
	var pod corev1.Pod
	if err := r.Client.Get(ctx, podKey, &pod); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !pod.DeletionTimestamp.IsZero() {
		return nil
	}
	agent := findPodContainer(&pod, agentContainerName)
	if agent == nil {
		return r.recordScalingError(
			ctx, client.ObjectKeyFromObject(session), "agent container is unavailable",
		)
	}
	policy, err := resourcepolicy.ForTier(session.Spec.Tier)
	if err != nil {
		return r.recordScalingError(ctx, client.ObjectKeyFromObject(session), err.Error())
	}
	if scaling.Mode == bosunv1alpha1.ResourceScalingModeAuto {
		window := r.window(session.UID)
		if window.Prepare(pod.UID, session.Generation) {
			r.clearFailedAttempt(session.UID)
		}
	}

	desired := agent.Resources.Limits.DeepCopy()
	if desired == nil {
		desired = corev1.ResourceList{}
	}
	switch scaling.Mode {
	case bosunv1alpha1.ResourceScalingModeManual:
		if scaling.ManualLimits == nil {
			return r.recordScalingError(
				ctx, client.ObjectKeyFromObject(session), "Manual mode has no manualLimits",
			)
		}
		if err := resourcepolicy.ValidateManualLimits(
			session.Spec.Tier, *scaling.ManualLimits,
		); err != nil {
			return r.recordScalingError(ctx, client.ObjectKeyFromObject(session), err.Error())
		}
		desired[corev1.ResourceCPU] = *resource.NewMilliQuantity(
			scaling.ManualLimits.CPUMillicores, resource.DecimalSI,
		)
		desired[corev1.ResourceMemory] = *resource.NewQuantity(
			scaling.ManualLimits.MemoryBytes, resource.BinarySI,
		)
	case bosunv1alpha1.ResourceScalingModeAuto:
		if desired.Cpu().IsZero() {
			desired[corev1.ResourceCPU] = *resource.NewMilliQuantity(
				policy.DefaultCPULimit, resource.DecimalSI,
			)
		}
		desired[corev1.ResourceMemory] = *resource.NewQuantity(
			policy.DefaultMemoryLimitBytes, resource.BinarySI,
		)
	default:
		return r.recordScalingError(
			ctx,
			client.ObjectKeyFromObject(session),
			fmt.Sprintf("unsupported resource scaling mode %q", scaling.Mode),
		)
	}

	if !resourceListsEqual(agent.Resources.Limits, desired) {
		return r.applyResize(
			ctx, session, &pod, scaling, desired, agent.Resources.Limits,
		)
	}
	if scaling.Mode == bosunv1alpha1.ResourceScalingModeManual {
		r.clearFailedAttempt(session.UID)
		return r.updateScalingStatus(
			ctx,
			client.ObjectKeyFromObject(session),
			func(status *bosunv1alpha1.ResourceScalingStatus) {
				status.LastError = ""
			},
		)
	}
	return r.reconcileAutoCPU(ctx, session, &pod, policy)
}

func (r *ResourceAutoscaler) reconcileAutoCPU(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
	policy resourcepolicy.TierPolicy,
) error {
	key := client.ObjectKeyFromObject(session)
	window := r.window(session.UID)
	if !autoObservationAllowed(session, pod) {
		return r.updateAutoStatus(
			ctx, key, session.UID,
			bosunv1alpha1.ResourceLoadClassUnknown, 0, "", false,
		)
	}
	actual, available := actualAgentResources(pod)
	if !available || actual.Limits.Cpu().MilliValue() <= 0 {
		return r.updateAutoStatus(
			ctx,
			key,
			session.UID,
			bosunv1alpha1.ResourceLoadClassUnknown,
			0,
			"agent actual CPU limit is unavailable",
			true,
		)
	}
	metric, err := r.Metrics.GetAgentPodMetric(ctx, pod.Namespace, pod.Name)
	if err != nil {
		return r.updateAutoStatus(
			ctx,
			key,
			session.UID,
			bosunv1alpha1.ResourceLoadClassUnknown,
			0,
			fmt.Sprintf("read PodMetrics: %v", err),
			true,
		)
	}
	now := r.now()
	if metric.Timestamp.IsZero() {
		return r.updateAutoStatus(
			ctx, key, session.UID, bosunv1alpha1.ResourceLoadClassUnknown, 0,
			"PodMetrics timestamp is unavailable", true,
		)
	}
	if now.Sub(metric.Timestamp) > resourcepolicy.MetricsMaxAge {
		return r.updateAutoStatus(
			ctx, key, session.UID, bosunv1alpha1.ResourceLoadClassUnknown, 0,
			fmt.Sprintf("PodMetrics is older than %s", resourcepolicy.MetricsMaxAge), true,
		)
	}
	window.Add(resourcepolicy.ResourceSample{
		PodUID:         pod.UID,
		ObservedAt:     metric.Timestamp,
		MetricWindow:   metric.Window,
		CPUUsage:       metric.CPUUsageMillicores,
		ActualCPULimit: actual.Limits.Cpu().MilliValue(),
	})
	loadClass, target := resourcepolicy.Recommendation(
		window.Samples(), actual.Limits.Cpu().MilliValue(), policy,
	)
	if err := r.updateAutoStatus(
		ctx, key, session.UID, loadClass, target, "", false,
	); err != nil {
		return err
	}
	if target == 0 || target == actual.Limits.Cpu().MilliValue() {
		return nil
	}
	if podHasActiveResize(pod) {
		return nil
	}
	desiredAgent := findPodContainer(pod, agentContainerName)
	if desiredAgent == nil ||
		desiredAgent.Resources.Limits.Cpu().MilliValue() != actual.Limits.Cpu().MilliValue() {
		return nil
	}
	switch loadClass {
	case bosunv1alpha1.ResourceLoadClassCPUHigh:
		if !cooldownElapsed(session.Status.ResourceScaling, now, r.ScaleUpCooldown) {
			return nil
		}
	case bosunv1alpha1.ResourceLoadClassCPULow:
		if !workStateAllowsScaleDown(session) ||
			!cooldownElapsed(session.Status.ResourceScaling, now, r.ScaleDownCooldown) {
			return nil
		}
	default:
		return nil
	}

	desired := desiredAgent.Resources.Limits.DeepCopy()
	desired[corev1.ResourceCPU] = *resource.NewMilliQuantity(target, resource.DecimalSI)
	return r.applyResize(
		ctx,
		session,
		pod,
		session.Spec.EffectiveResourceScaling(),
		desired,
		desiredAgent.Resources.Limits,
	)
}

func (r *ResourceAutoscaler) applyResize(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
	scaling bosunv1alpha1.ResourceScalingSpec,
	desired corev1.ResourceList,
	previous corev1.ResourceList,
) error {
	intent := failedResizeIntent{
		PodUID: pod.UID, Mode: scaling.Mode,
		CPU: desired.Cpu().MilliValue(), Memory: desired.Memory().Value(),
	}
	if r.retrySuppressed(session.UID, intent) {
		return nil
	}

	var latestSession bosunv1alpha1.AgentSession
	if err := r.Client.Get(
		ctx, client.ObjectKeyFromObject(session), &latestSession,
	); err != nil {
		return err
	}
	latestScaling := latestSession.Spec.EffectiveResourceScaling()
	if latestSession.Generation != session.Generation ||
		!reflect.DeepEqual(latestScaling, scaling) {
		return nil
	}
	var latestPod corev1.Pod
	if err := r.Client.Get(
		ctx, client.ObjectKeyFromObject(pod), &latestPod,
	); err != nil {
		return client.IgnoreNotFound(err)
	}
	latestAgent := findPodContainer(&latestPod, agentContainerName)
	if latestPod.UID != pod.UID ||
		latestAgent == nil ||
		!resourceListsEqual(latestAgent.Resources.Limits, previous) ||
		!actualCPUUnchanged(pod, &latestPod) {
		return nil
	}
	if scaling.Mode == bosunv1alpha1.ResourceScalingModeAuto &&
		podHasActiveResize(&latestPod) {
		return nil
	}
	latestAgent.Resources.Limits = desired.DeepCopy()
	if _, err := r.Resizer.UpdateResize(ctx, &latestPod); err != nil {
		intent.AttemptedAt = r.now()
		r.recordFailedAttempt(session.UID, intent)
		message := fmt.Sprintf("update Pod resize: %v", err)
		_ = r.recordScalingError(ctx, client.ObjectKeyFromObject(session), message)
		return fmt.Errorf("%s: %w", client.ObjectKeyFromObject(pod), err)
	}
	r.clearFailedAttempt(session.UID)
	now := metav1.NewTime(r.now())
	return r.updateScalingStatus(
		ctx,
		client.ObjectKeyFromObject(session),
		func(status *bosunv1alpha1.ResourceScalingStatus) {
			status.LastAppliedAt = &now
			status.LastError = ""
		},
	)
}

func (r *ResourceAutoscaler) updateAutoStatus(
	ctx context.Context,
	key types.NamespacedName,
	uid types.UID,
	loadClass bosunv1alpha1.ResourceLoadClass,
	target int64,
	message string,
	isError bool,
) error {
	return r.updateScalingStatus(
		ctx,
		key,
		func(status *bosunv1alpha1.ResourceScalingStatus) {
			status.LoadClass = loadClass
			status.RecommendedCPUMillicores = target
			if isError {
				status.LastError = safeConditionMessage(message)
			} else if _, failed := r.failedAttempts[uid]; !failed {
				status.LastError = ""
			}
		},
	)
}

func (r *ResourceAutoscaler) recordScalingError(
	ctx context.Context,
	key types.NamespacedName,
	message string,
) error {
	if err := r.updateScalingStatus(
		ctx,
		key,
		func(status *bosunv1alpha1.ResourceScalingStatus) {
			status.LastError = safeConditionMessage(message)
		},
	); err != nil {
		return err
	}
	return fmt.Errorf("%s", message)
}

func (r *ResourceAutoscaler) updateScalingStatus(
	ctx context.Context,
	key types.NamespacedName,
	mutate func(*bosunv1alpha1.ResourceScalingStatus),
) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var current bosunv1alpha1.AgentSession
		if err := r.Client.Get(ctx, key, &current); err != nil {
			return err
		}
		desired := &bosunv1alpha1.ResourceScalingStatus{}
		if current.Status.ResourceScaling != nil {
			desired = current.Status.ResourceScaling.DeepCopy()
		}
		mutate(desired)
		if reflect.DeepEqual(current.Status.ResourceScaling, desired) {
			return nil
		}
		current.Status.ResourceScaling = desired
		return r.Client.Status().Update(ctx, &current)
	})
}

func autoObservationAllowed(
	session *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
) bool {
	if session.Spec.DesiredState != bosunv1alpha1.DesiredStateRunning ||
		pod.Status.Phase != corev1.PodRunning ||
		pod.Spec.NodeName == "" {
		return false
	}
	return session.Status.Phase == bosunv1alpha1.AgentSessionPhaseRunning ||
		session.Status.Phase == bosunv1alpha1.AgentSessionPhaseIdle
}

func actualAgentResources(pod *corev1.Pod) (*corev1.ResourceRequirements, bool) {
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]
		if status.Name == agentContainerName && status.Resources != nil {
			return status.Resources, true
		}
	}
	return nil, false
}

func actualCPUUnchanged(previous, latest *corev1.Pod) bool {
	previousResources, previousAvailable := actualAgentResources(previous)
	latestResources, latestAvailable := actualAgentResources(latest)
	if previousAvailable != latestAvailable {
		return false
	}
	if !previousAvailable {
		return true
	}
	return previousResources.Limits.Cpu().Cmp(*latestResources.Limits.Cpu()) == 0
}

func podHasActiveResize(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		condition := &pod.Status.Conditions[i]
		if (condition.Type == corev1.PodResizePending ||
			condition.Type == corev1.PodResizeInProgress) &&
			condition.Status != corev1.ConditionFalse {
			return true
		}
	}
	return false
}

func workStateAllowsScaleDown(session *bosunv1alpha1.AgentSession) bool {
	for i := range session.Status.Conditions {
		condition := &session.Status.Conditions[i]
		if condition.Type != sessionReadyCondition ||
			condition.Status != metav1.ConditionTrue {
			continue
		}
		switch condition.Reason {
		case awaitingInputReason, "AgentStopped", "SessionIdle":
			return true
		default:
			return false
		}
	}
	return false
}

func cooldownElapsed(
	status *bosunv1alpha1.ResourceScalingStatus,
	now time.Time,
	cooldown time.Duration,
) bool {
	return status == nil ||
		status.LastAppliedAt == nil ||
		now.Sub(status.LastAppliedAt.Time) >= cooldown
}

func resourceListsEqual(left, right corev1.ResourceList) bool {
	return left.Cpu().Cmp(*right.Cpu()) == 0 &&
		left.Memory().Cmp(*right.Memory()) == 0
}

func findPodContainer(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

func (r *ResourceAutoscaler) window(uid types.UID) *resourcepolicy.SampleWindow {
	if r.windows == nil {
		r.windows = map[types.UID]*resourcepolicy.SampleWindow{}
	}
	if r.windows[uid] == nil {
		r.windows[uid] = &resourcepolicy.SampleWindow{}
	}
	return r.windows[uid]
}

func (r *ResourceAutoscaler) clearWindow(uid types.UID) {
	if r.windows != nil {
		delete(r.windows, uid)
	}
}

func (r *ResourceAutoscaler) retrySuppressed(
	uid types.UID,
	intent failedResizeIntent,
) bool {
	if r.failedAttempts == nil {
		return false
	}
	failed, ok := r.failedAttempts[uid]
	if !ok ||
		failed.PodUID != intent.PodUID ||
		failed.Mode != intent.Mode ||
		failed.CPU != intent.CPU ||
		failed.Memory != intent.Memory {
		return false
	}
	interval := r.RetryInterval
	if interval <= 0 {
		interval = defaultResizeRetryInterval
	}
	return r.now().Sub(failed.AttemptedAt) < interval
}

func (r *ResourceAutoscaler) recordFailedAttempt(
	uid types.UID,
	intent failedResizeIntent,
) {
	if r.failedAttempts == nil {
		r.failedAttempts = map[types.UID]failedResizeIntent{}
	}
	r.failedAttempts[uid] = intent
}

func (r *ResourceAutoscaler) clearFailedAttempt(uid types.UID) {
	if r.failedAttempts != nil {
		delete(r.failedAttempts, uid)
	}
}

func (r *ResourceAutoscaler) pruneState(seen map[types.UID]struct{}) {
	for uid := range r.windows {
		if _, ok := seen[uid]; !ok {
			delete(r.windows, uid)
		}
	}
	for uid := range r.failedAttempts {
		if _, ok := seen[uid]; !ok {
			delete(r.failedAttempts, uid)
		}
	}
}

func (r *ResourceAutoscaler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

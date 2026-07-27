package controller

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
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
	CPU         int64
	Memory      int64
	AttemptedAt time.Time
}

type autoscaleSession struct {
	key       string
	session   *bosunv1alpha1.AgentSession
	pod       *corev1.Pod
	priority  int
	current   int64
	target    int64
	demand    int64
	scaleUp   bool
	needsSync bool
}

// ResourceAutoscaler samples CPU load and plans complete, fair allocations per
// node before applying any Pod resize.
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

// +kubebuilder:rbac:groups=bosun.io,resources=agentsessions,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=bosun.io,resources=agentsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/resize,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes/proxy,verbs=get

// Start runs one reconciliation immediately and then at the configured interval.
func (r *ResourceAutoscaler) Start(ctx context.Context) error {
	if r.Client == nil || r.Resizer == nil || r.Metrics == nil {
		return fmt.Errorf(
			"resource autoscaler requires Kubernetes client, Pod resizer, and Kubelet metrics reader",
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
	var pods corev1.PodList
	var nodes corev1.NodeList
	if err := r.Client.List(ctx, &sessions); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list AgentSessions for resource scaling")
		return
	}
	if err := r.Client.List(ctx, &pods); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list Pods for resource scaling")
		return
	}
	if err := r.Client.List(ctx, &nodes); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list Nodes for resource scaling")
		return
	}
	r.Metrics.BeginCycle()
	seen := make(map[types.UID]struct{}, len(sessions.Items))
	sessionsByID := make(map[string]*bosunv1alpha1.AgentSession, len(sessions.Items))
	for i := range sessions.Items {
		session := &sessions.Items[i]
		if !session.DeletionTimestamp.IsZero() {
			continue
		}
		seen[session.UID] = struct{}{}
		sessionsByID[session.Spec.SessionID] = session
	}

	byNode := make(map[string][]*autoscaleSession)
	for i := range pods.Items {
		pod := &pods.Items[i]
		sessionID := pod.Labels[sessionLabel]
		session := sessionsByID[sessionID]
		if session == nil || pod.Spec.NodeName == "" || !pod.DeletionTimestamp.IsZero() {
			continue
		}
		state, err := r.observeSession(ctx, session, pod)
		if err != nil {
			logf.FromContext(ctx).Error(
				err, "Could not observe AgentSession resources",
				"namespace", session.Namespace, "name", session.Name,
			)
			continue
		}
		if state != nil {
			byNode[pod.Spec.NodeName] = append(byNode[pod.Spec.NodeName], state)
		}
	}

	nodeByName := make(map[string]*corev1.Node, len(nodes.Items))
	for i := range nodes.Items {
		nodeByName[nodes.Items[i].Name] = &nodes.Items[i]
	}
	reservedCPU := pendingReservationCPU(sessions.Items, pods.Items)
	for nodeName, states := range byNode {
		node := nodeByName[nodeName]
		if node == nil {
			continue
		}
		free := max(nodeFreeCPU(node, pods.Items)-reservedCPU[nodeName], 0)
		r.planNode(states, free)
		r.applyNodePlan(ctx, states)
	}
	r.reserveWaitingSession(ctx, sessions.Items, pods.Items, nodes.Items, byNode)
	r.pruneState(seen)
}

type nodeReservationCandidate struct {
	node        *corev1.Node
	free        int64
	reclaimable int64
}

func (r *ResourceAutoscaler) reserveWaitingSession(
	ctx context.Context,
	sessions []bosunv1alpha1.AgentSession,
	pods []corev1.Pod,
	nodes []corev1.Node,
	byNode map[string][]*autoscaleSession,
) {
	podSessions := make(map[string]struct{}, len(pods))
	for i := range pods {
		if sessionID := pods[i].Labels[sessionLabel]; sessionID != "" {
			podSessions[sessionID] = struct{}{}
		}
	}
	reservedCPU := pendingReservationCPU(sessions, pods)
	waiting := make([]*bosunv1alpha1.AgentSession, 0)
	for i := range sessions {
		session := &sessions[i]
		if !session.DeletionTimestamp.IsZero() ||
			session.Spec.DesiredState != bosunv1alpha1.DesiredStateRunning {
			continue
		}
		if nodeName := session.Annotations[schedulingNodeAnnotation]; nodeName != "" {
			continue
		}
		if _, exists := podSessions[session.Spec.SessionID]; !exists {
			waiting = append(waiting, session)
		}
	}
	slices.SortFunc(waiting, func(left, right *bosunv1alpha1.AgentSession) int {
		if order := cmp.Compare(
			priorityRank(right.Spec.PriorityClassName),
			priorityRank(left.Spec.PriorityClassName),
		); order != 0 {
			return order
		}
		if order := left.CreationTimestamp.Compare(right.CreationTimestamp.Time); order != 0 {
			return order
		}
		return cmp.Compare(
			client.ObjectKeyFromObject(left).String(),
			client.ObjectKeyFromObject(right).String(),
		)
	})
	if len(waiting) == 0 {
		return
	}

	candidates := make([]nodeReservationCandidate, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		if !nodeEligibleForAgent(node) {
			continue
		}
		nodeIsChanging := false
		for _, state := range byNode[node.Name] {
			if state.target != state.current || state.needsSync {
				nodeIsChanging = true
				break
			}
		}
		if nodeIsChanging {
			continue
		}
		free := max(nodeFreeCPU(node, pods)-reservedCPU[node.Name], 0)
		reclaimable := int64(0)
		for _, state := range byNode[node.Name] {
			if state.priority > 1 {
				reclaimable += max(state.current-agentBaseCPUMillicores, 0)
			}
		}
		if free+reclaimable >= agentBaseCPUMillicores {
			candidates = append(candidates, nodeReservationCandidate{
				node: node, free: free, reclaimable: reclaimable,
			})
		}
	}
	slices.SortFunc(candidates, func(left, right nodeReservationCandidate) int {
		leftReclaim := max(agentBaseCPUMillicores-left.free, 0)
		rightReclaim := max(agentBaseCPUMillicores-right.free, 0)
		if order := cmp.Compare(leftReclaim, rightReclaim); order != 0 {
			return order
		}
		leftRemaining := left.free + left.reclaimable - agentBaseCPUMillicores
		rightRemaining := right.free + right.reclaimable - agentBaseCPUMillicores
		if order := cmp.Compare(rightRemaining, leftRemaining); order != 0 {
			return order
		}
		return cmp.Compare(left.node.Name, right.node.Name)
	})
	if len(candidates) == 0 {
		return
	}
	selected := candidates[0]
	required := max(agentBaseCPUMillicores-selected.free, 0)
	if required > 0 && !r.reclaimForReservation(
		ctx, byNode[selected.node.Name], required,
	) {
		return
	}
	session := waiting[0]
	original := session.DeepCopy()
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[schedulingNodeAnnotation] = selected.node.Name
	if err := r.Client.Patch(ctx, session, client.MergeFrom(original)); err != nil {
		logf.FromContext(ctx).Error(
			err, "Could not reserve Node for AgentSession",
			"namespace", session.Namespace, "name", session.Name,
		)
	}
}

func pendingReservationCPU(
	sessions []bosunv1alpha1.AgentSession,
	pods []corev1.Pod,
) map[string]int64 {
	scheduled := make(map[string]struct{}, len(pods))
	for i := range pods {
		if pods[i].Spec.NodeName == "" {
			continue
		}
		if sessionID := pods[i].Labels[sessionLabel]; sessionID != "" {
			scheduled[sessionID] = struct{}{}
		}
	}
	reserved := make(map[string]int64)
	for i := range sessions {
		session := &sessions[i]
		nodeName := session.Annotations[schedulingNodeAnnotation]
		if nodeName == "" || !session.DeletionTimestamp.IsZero() ||
			session.Spec.DesiredState != bosunv1alpha1.DesiredStateRunning {
			continue
		}
		if _, exists := scheduled[session.Spec.SessionID]; !exists {
			reserved[nodeName] += agentBaseCPUMillicores
		}
	}
	return reserved
}

func (r *ResourceAutoscaler) reclaimForReservation(
	ctx context.Context,
	states []*autoscaleSession,
	required int64,
) bool {
	for _, priority := range []int{2, 3} {
		capacities := map[string]int64{}
		byKey := map[string]*autoscaleSession{}
		for _, state := range states {
			if state.priority != priority {
				continue
			}
			extra := max(state.current-agentBaseCPUMillicores, 0)
			if extra > 0 {
				capacities[state.key] = extra
				byKey[state.key] = state
			}
		}
		available := int64(0)
		for _, extra := range capacities {
			available += extra
		}
		reclaim := min(required, available)
		allocations := proportionalCPU(reclaim, capacities)
		keys := make([]string, 0, len(allocations))
		for key := range allocations {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			amount := allocations[key]
			state := byKey[key]
			state.target = state.current - amount
			applied, err := r.resizeState(ctx, state)
			if err != nil || !applied {
				logf.FromContext(ctx).Error(
					err, "Could not reclaim Agent CPU for pending session",
					"session", state.key,
				)
				return false
			}
		}
		required -= reclaim
		if required == 0 {
			return true
		}
	}
	return false
}

func nodeEligibleForAgent(node *corev1.Node) bool {
	if node.Spec.Unschedulable ||
		node.Labels[agentNodeRoleLabel] != agentNodeRoleValue {
		return false
	}
	ready := false
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady &&
			node.Status.Conditions[i].Status == corev1.ConditionTrue {
			ready = true
			break
		}
	}
	if !ready {
		return false
	}
	tolerations := agentTolerations()
	for i := range node.Spec.Taints {
		taint := &node.Spec.Taints[i]
		if taint.Effect != corev1.TaintEffectNoSchedule &&
			taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		tolerated := false
		for j := range tolerations {
			if tolerates(&tolerations[j], taint) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	return true
}

func tolerates(toleration *corev1.Toleration, taint *corev1.Taint) bool {
	if toleration.Effect != "" && toleration.Effect != taint.Effect {
		return false
	}
	switch toleration.Operator {
	case corev1.TolerationOpExists:
		return toleration.Key == "" || toleration.Key == taint.Key
	case corev1.TolerationOpEqual, "":
		return toleration.Key == taint.Key && toleration.Value == taint.Value
	default:
		return false
	}
}

// reconcileSession remains a focused single-session entry point for tests and
// resource repair. Fair multi-session allocation is performed by runOnce.
func (r *ResourceAutoscaler) reconcileSession(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
) error {
	var pod corev1.Pod
	if err := r.Client.Get(ctx, types.NamespacedName{
		Namespace: session.Namespace,
		Name:      sessionidentity.PodName(session.Spec.SessionID),
	}, &pod); err != nil {
		return client.IgnoreNotFound(err)
	}
	state, err := r.observeSession(ctx, session, &pod)
	if err != nil || state == nil {
		return err
	}
	state.target = state.demand
	if state.target == state.current && !state.needsSync {
		return nil
	}
	_, err = r.resizeState(ctx, state)
	return err
}

func (r *ResourceAutoscaler) observeSession(
	ctx context.Context,
	session *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
) (*autoscaleSession, error) {
	agent := findPodContainer(pod, agentContainerName)
	if agent == nil {
		return nil, r.recordScalingError(
			ctx, client.ObjectKeyFromObject(session), "agent container is unavailable",
		)
	}
	policy := resourcepolicy.Policy()
	current := agent.Resources.Limits.Cpu().MilliValue()
	if current <= 0 {
		current = policy.MinCPULimit
	}
	state := &autoscaleSession{
		key:     client.ObjectKeyFromObject(session).String(),
		session: session, pod: pod,
		priority: priorityRank(session.Spec.PriorityClassName),
		current:  current, target: current, demand: current,
		needsSync: agent.Resources.Requests.Cpu().MilliValue() != current ||
			agent.Resources.Limits.Memory().Value() != policy.DefaultMemoryLimitBytes,
	}
	window := r.window(session.UID)
	if window.Prepare(agentMetricIdentity(pod), session.Generation) {
		r.clearFailedAttempt(session.UID)
	}

	if state.priority == 1 {
		r.clearWindow(session.UID)
		state.target = policy.MinCPULimit
		state.demand = policy.MinCPULimit
		return state, r.updateAutoStatus(
			ctx, client.ObjectKeyFromObject(session), session.UID,
			bosunv1alpha1.ResourceLoadClassUnknown, policy.MinCPULimit, "", false,
		)
	}
	if !autoObservationAllowed(session, pod) {
		return state, r.updateAutoStatus(
			ctx, client.ObjectKeyFromObject(session), session.UID,
			bosunv1alpha1.ResourceLoadClassUnknown, 0, "", false,
		)
	}
	actual, available := actualAgentResources(pod)
	if !available || actual.Limits.Cpu().MilliValue() <= 0 {
		return state, r.updateAutoStatus(
			ctx, client.ObjectKeyFromObject(session), session.UID,
			bosunv1alpha1.ResourceLoadClassUnknown, 0,
			"agent actual CPU limit is unavailable", true,
		)
	}
	metric, ready, err := r.Metrics.GetAgentPodMetric(ctx, pod)
	if err != nil {
		return state, r.updateAutoStatus(
			ctx, client.ObjectKeyFromObject(session), session.UID,
			bosunv1alpha1.ResourceLoadClassUnknown, 0,
			fmt.Sprintf("read Kubelet metrics: %v", err), true,
		)
	}
	if ready {
		now := r.now()
		if metric.Timestamp.IsZero() {
			return state, r.updateAutoStatus(
				ctx, client.ObjectKeyFromObject(session), session.UID,
				bosunv1alpha1.ResourceLoadClassUnknown, 0,
				"Kubelet metric timestamp is unavailable", true,
			)
		}
		if now.Sub(metric.Timestamp) > resourcepolicy.MetricsMaxAge {
			return state, r.updateAutoStatus(
				ctx, client.ObjectKeyFromObject(session), session.UID,
				bosunv1alpha1.ResourceLoadClassUnknown, 0,
				fmt.Sprintf("Kubelet metric is older than %s", resourcepolicy.MetricsMaxAge), true,
			)
		}
		window.Add(resourcepolicy.ResourceSample{
			PodUID: agentMetricIdentity(pod), ObservedAt: metric.Timestamp,
			MetricWindow: metric.Window, CPUUsage: metric.CPUUsageMillicores,
			ActualCPULimit: actual.Limits.Cpu().MilliValue(),
		})
	}
	loadClass, target := resourcepolicy.Recommendation(window.Samples(), current, policy)
	if err := r.updateAutoStatus(
		ctx, client.ObjectKeyFromObject(session), session.UID, loadClass, target, "", false,
	); err != nil {
		return nil, err
	}
	if target == 0 || target == current || podHasActiveResize(pod) {
		return state, nil
	}
	now := r.now()
	switch loadClass {
	case bosunv1alpha1.ResourceLoadClassCPUHigh:
		if cooldownElapsed(session.Status.ResourceScaling, now, r.ScaleUpCooldown) {
			state.demand = target
			state.scaleUp = true
		}
	case bosunv1alpha1.ResourceLoadClassCPULow:
		if workStateAllowsScaleDown(session) &&
			cooldownElapsed(session.Status.ResourceScaling, now, r.ScaleDownCooldown) {
			state.target = target
			state.demand = target
		}
	}
	return state, nil
}

func (r *ResourceAutoscaler) planNode(states []*autoscaleSession, free int64) {
	slices.SortFunc(states, func(left, right *autoscaleSession) int {
		return cmp.Compare(left.key, right.key)
	})
	// Explicit low-load reductions and the low-priority fixed quota are
	// released before either priority receives expansion capacity.
	for _, state := range states {
		if state.target < state.current {
			free += state.current - state.target
		}
	}

	normal := statesAtPriority(states, 2)
	high := statesAtPriority(states, 3)
	if hasScaleUp(high) {
		free = planFairPriority(high, normal, free, true)
	}
	if hasScaleUp(normal) {
		_ = planFairPriority(normal, nil, free, false)
	}
}

func planFairPriority(
	states []*autoscaleSession,
	reclaimable []*autoscaleSession,
	free int64,
	allowReclaim bool,
) int64 {
	demands := make([]cpuDemand, 0, len(states))
	currentExtras := int64(0)
	demandExtras := int64(0)
	for _, state := range states {
		currentExtra := max(state.target-agentBaseCPUMillicores, 0)
		limitExtra := max(state.demand-agentBaseCPUMillicores, 0)
		if currentExtra == 0 && !state.scaleUp {
			continue
		}
		currentExtras += currentExtra
		demandExtras += limitExtra
		demands = append(demands, cpuDemand{
			key: state.key, limit: limitExtra,
		})
	}
	reclaimableExtras := int64(0)
	for _, state := range reclaimable {
		reclaimableExtras += max(state.target-agentBaseCPUMillicores, 0)
	}
	pool := currentExtras + free
	if allowReclaim {
		pool += reclaimableExtras
	}
	pool = min(pool, demandExtras)
	allocations := maxMinCPU(pool, demands)
	newExtras := int64(0)
	for _, state := range states {
		if extra, ok := allocations[state.key]; ok {
			state.target = agentBaseCPUMillicores + extra
			newExtras += extra
		}
	}
	reclaim := max(newExtras-currentExtras-free, 0)
	if reclaim > 0 {
		capacities := make(map[string]int64, len(reclaimable))
		for _, state := range reclaimable {
			capacities[state.key] = max(state.target-agentBaseCPUMillicores, 0)
		}
		for key, amount := range proportionalCPU(reclaim, capacities) {
			for _, state := range reclaimable {
				if state.key == key {
					state.target -= amount
					break
				}
			}
		}
	}
	return max(free-(newExtras-currentExtras)+reclaim, 0)
}

func (r *ResourceAutoscaler) applyNodePlan(ctx context.Context, states []*autoscaleSession) {
	decreases := make([]*autoscaleSession, 0)
	increases := make([]*autoscaleSession, 0)
	for _, state := range states {
		switch {
		case state.target < state.current:
			decreases = append(decreases, state)
		case state.target > state.current || state.needsSync:
			increases = append(increases, state)
		}
	}
	failed := false
	for _, state := range decreases {
		applied, err := r.resizeState(ctx, state)
		if err != nil || !applied {
			failed = true
			logf.FromContext(ctx).Error(err, "Could not reduce Agent CPU", "session", state.key)
		}
	}
	if failed {
		return
	}
	for _, state := range increases {
		if _, err := r.resizeState(ctx, state); err != nil {
			logf.FromContext(ctx).Error(err, "Could not increase Agent CPU", "session", state.key)
		}
	}
}

func (r *ResourceAutoscaler) resizeState(
	ctx context.Context,
	state *autoscaleSession,
) (bool, error) {
	policy := resourcepolicy.Policy()
	target := min(max(state.target, policy.MinCPULimit), policy.MaxCPULimit)
	intent := failedResizeIntent{
		PodUID: state.pod.UID, CPU: target, Memory: policy.DefaultMemoryLimitBytes,
	}
	if r.retrySuppressed(state.session.UID, intent) {
		return false, nil
	}
	var latestSession bosunv1alpha1.AgentSession
	if err := r.Client.Get(
		ctx, client.ObjectKeyFromObject(state.session), &latestSession,
	); err != nil {
		return false, err
	}
	if latestSession.Generation != state.session.Generation {
		return false, nil
	}
	var latestPod corev1.Pod
	if err := r.Client.Get(
		ctx, client.ObjectKeyFromObject(state.pod), &latestPod,
	); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	agent := findPodContainer(&latestPod, agentContainerName)
	previous := findPodContainer(state.pod, agentContainerName)
	if latestPod.UID != state.pod.UID || agent == nil || previous == nil ||
		!resourceRequirementsEqual(agent.Resources, previous.Resources) ||
		!actualCPUUnchanged(state.pod, &latestPod) ||
		podHasActiveResize(&latestPod) {
		return false, nil
	}
	if agent.Resources.Limits == nil {
		agent.Resources.Limits = corev1.ResourceList{}
	}
	if agent.Resources.Requests == nil {
		agent.Resources.Requests = corev1.ResourceList{}
	}
	agent.Resources.Limits[corev1.ResourceCPU] =
		*resource.NewMilliQuantity(target, resource.DecimalSI)
	agent.Resources.Requests[corev1.ResourceCPU] =
		*resource.NewMilliQuantity(target, resource.DecimalSI)
	agent.Resources.Limits[corev1.ResourceMemory] =
		*resource.NewQuantity(policy.DefaultMemoryLimitBytes, resource.BinarySI)
	if _, err := r.Resizer.UpdateResize(ctx, &latestPod); err != nil {
		intent.AttemptedAt = r.now()
		r.recordFailedAttempt(state.session.UID, intent)
		message := fmt.Sprintf("update Pod resize: %v", err)
		_ = r.recordScalingError(
			ctx, client.ObjectKeyFromObject(state.session), message,
		)
		return false, fmt.Errorf("%s: %w", client.ObjectKeyFromObject(state.pod), err)
	}
	r.clearFailedAttempt(state.session.UID)
	r.clearWindow(state.session.UID)
	now := metav1.NewTime(r.now())
	err := r.updateScalingStatus(
		ctx, client.ObjectKeyFromObject(state.session),
		func(status *bosunv1alpha1.ResourceScalingStatus) {
			status.LastAppliedAt = &now
			status.LastError = ""
		},
	)
	return err == nil, err
}

func statesAtPriority(states []*autoscaleSession, priority int) []*autoscaleSession {
	result := make([]*autoscaleSession, 0)
	for _, state := range states {
		if state.priority == priority {
			result = append(result, state)
		}
	}
	return result
}

func hasScaleUp(states []*autoscaleSession) bool {
	for _, state := range states {
		if state.scaleUp {
			return true
		}
	}
	return false
}

func priorityRank(className string) int {
	switch className {
	case highPriorityClass:
		return 3
	case normalPriorityClass:
		return 2
	default:
		return 1
	}
}

func nodeFreeCPU(node *corev1.Node, pods []corev1.Pod) int64 {
	used := int64(0)
	for i := range pods {
		pod := &pods[i]
		if pod.Spec.NodeName != node.Name ||
			pod.Status.Phase == corev1.PodSucceeded ||
			pod.Status.Phase == corev1.PodFailed {
			continue
		}
		used += podCPULimits(pod)
	}
	return max(node.Status.Allocatable.Cpu().MilliValue()-used, 0)
}

func podCPULimits(pod *corev1.Pod) int64 {
	total := int64(0)
	for i := range pod.Spec.InitContainers {
		total += pod.Spec.InitContainers[i].Resources.Limits.Cpu().MilliValue()
	}
	for i := range pod.Spec.Containers {
		total += pod.Spec.Containers[i].Resources.Limits.Cpu().MilliValue()
	}
	for i := range pod.Spec.EphemeralContainers {
		total += pod.Spec.EphemeralContainers[i].Resources.Limits.Cpu().MilliValue()
	}
	return total
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
		ctx, key,
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
		ctx, key,
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
		case agentWorkingReason, awaitingApprovalReason, awaitingChoiceReason:
			return false
		default:
			return true
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

func resourceRequirementsEqual(left, right corev1.ResourceRequirements) bool {
	return left.Limits.Cpu().Cmp(*right.Limits.Cpu()) == 0 &&
		left.Limits.Memory().Cmp(*right.Limits.Memory()) == 0 &&
		left.Requests.Cpu().Cmp(*right.Requests.Cpu()) == 0 &&
		left.Requests.Memory().Cmp(*right.Requests.Memory()) == 0
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

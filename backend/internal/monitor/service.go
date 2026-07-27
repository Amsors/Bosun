package monitor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"

	"github.com/Amsors/Bosun/backend/internal/session"
	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

const requestTimeout = 8 * time.Second

const agentContainerName = "agent"

type SessionStore interface {
	Get(context.Context, uuid.UUID, uuid.UUID) (session.Session, error)
}

type Service struct {
	sessions SessionStore
	owners   OwnerStore
	source   Source
	live     liveMetricSampler
	now      func() time.Time
}

func NewService(sessions SessionStore, owners OwnerStore, source Source) (*Service, error) {
	if sessions == nil || owners == nil || source == nil {
		return nil, errors.New("monitor service requires sessions, owners and Kubernetes source")
	}
	return &Service{
		sessions: sessions,
		owners:   owners,
		source:   source,
		now:      func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *Service) Session(ctx context.Context, userID, sessionID uuid.UUID) (SessionSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	record, err := s.sessions.Get(ctx, userID, sessionID)
	if err != nil {
		return SessionSnapshot{}, err
	}
	pod, err := s.source.GetPod(ctx, record.CRNamespace, sessionidentity.PodName(record.ID.String()))
	if apierrors.IsNotFound(err) {
		return SessionSnapshot{}, session.ErrNotFound
	}
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("get session Pod: %w", err)
	}
	metric, metricErr := s.source.GetPodMetric(ctx, pod.Namespace, pod.Name)
	metricPtr := podMetricPointer(metric, metricErr)
	if pod.Spec.NodeName != "" {
		if counters, liveErr := s.source.GetNodePodCounters(ctx, pod.Spec.NodeName); liveErr == nil {
			if liveMetric, ready := s.live.podMetric(
				pod.Namespace+"/"+pod.Name,
				counters[pod.Namespace+"/"+pod.Name],
			); ready {
				metricPtr = &liveMetric
			}
		}
	}
	if metricPtr == nil && metricErr != nil && !metricsUnavailable(metricErr) {
		return SessionSnapshot{}, fmt.Errorf("get session Pod metrics: %w", metricErr)
	}
	cr, err := s.source.GetAgentSession(ctx, record.CRNamespace, record.CRName)
	if apierrors.IsNotFound(err) {
		return SessionSnapshot{}, session.ErrNotFound
	}
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("get session AgentSession: %w", err)
	}
	return SessionSnapshot{
		ObservedAt:       s.now(),
		MetricsAvailable: metricPtr != nil,
		Pod:              snapshotPod(pod, metricPtr, nil, map[string]*bosunv1alpha1.AgentSession{cr.Spec.SessionID: cr}),
	}, nil
}

func (s *Service) Cluster(ctx context.Context) (ClusterSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	nodes, err := s.source.ListNodes(ctx)
	if err != nil {
		return ClusterSnapshot{}, fmt.Errorf("list Nodes: %w", err)
	}
	pods, err := s.source.ListPods(ctx)
	if err != nil {
		return ClusterSnapshot{}, fmt.Errorf("list Pods: %w", err)
	}
	owners, err := s.owners.ListAgentOwners(ctx)
	if err != nil {
		return ClusterSnapshot{}, err
	}
	sessions, err := s.source.ListAgentSessions(ctx)
	if err != nil {
		return ClusterSnapshot{}, fmt.Errorf("list AgentSessions: %w", err)
	}
	scalingBySession := make(map[string]*bosunv1alpha1.AgentSession, len(sessions))
	for i := range sessions {
		scalingBySession[sessions[i].Spec.SessionID] = &sessions[i]
	}

	podMetrics, podMetricsErr := s.source.ListPodMetrics(ctx)
	if podMetricsErr != nil && !metricsUnavailable(podMetricsErr) {
		return ClusterSnapshot{}, fmt.Errorf("list Pod metrics: %w", podMetricsErr)
	}
	nodeMetrics, nodeMetricsErr := s.source.ListNodeMetrics(ctx)
	if nodeMetricsErr != nil && !metricsUnavailable(nodeMetricsErr) {
		return ClusterSnapshot{}, fmt.Errorf("list Node metrics: %w", nodeMetricsErr)
	}
	if podMetrics == nil {
		podMetrics = map[string]PodMetric{}
	}
	livePodMetricsAvailable := false
	for i := range nodes {
		counters, liveErr := s.source.GetNodePodCounters(ctx, nodes[i].Name)
		if liveErr != nil {
			continue
		}
		for podKey, counter := range counters {
			if liveMetric, ready := s.live.podMetric(podKey, counter); ready {
				podMetrics[podKey] = liveMetric
				livePodMetricsAvailable = true
			}
		}
	}

	result := ClusterSnapshot{
		ObservedAt:           s.now(),
		PodMetricsAvailable:  podMetricsErr == nil || livePodMetricsAvailable,
		NodeMetricsAvailable: nodeMetricsErr == nil,
		Nodes:                make([]NodeSnapshot, 0, len(nodes)),
		Pods:                 make([]PodSnapshot, 0, len(pods)),
	}
	for i := range nodes {
		var usage *Resources
		if value, ok := nodeMetrics[nodes[i].Name]; ok {
			copy := value
			usage = &copy
		}
		result.Nodes = append(result.Nodes, snapshotNode(&nodes[i], usage))
	}
	for i := range pods {
		var metric *PodMetric
		if value, ok := podMetrics[pods[i].Namespace+"/"+pods[i].Name]; ok {
			copy := value
			metric = &copy
		}
		result.Pods = append(result.Pods, snapshotPod(&pods[i], metric, owners, scalingBySession))
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Name < result.Nodes[j].Name })
	sort.Slice(result.Pods, func(i, j int) bool {
		if result.Pods[i].Namespace == result.Pods[j].Namespace {
			return result.Pods[i].Name < result.Pods[j].Name
		}
		return result.Pods[i].Namespace < result.Pods[j].Namespace
	})
	return result, nil
}

func podMetricPointer(metric PodMetric, err error) *PodMetric {
	if err != nil {
		return nil
	}
	return &metric
}

func snapshotPod(
	pod *corev1.Pod,
	metric *PodMetric,
	owners map[string]AgentOwner,
	scalingBySession map[string]*bosunv1alpha1.AgentSession,
) PodSnapshot {
	result := PodSnapshot{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Phase:     string(pod.Status.Phase),
		NodeName:  pod.Spec.NodeName,
		CreatedAt: pod.CreationTimestamp.Time,
		Ready:     podReady(pod),
	}
	if !pod.DeletionTimestamp.IsZero() {
		result.Phase = "Terminating"
	}
	result.Resize = podResizeSnapshot(pod)
	if metric != nil {
		if !metric.ObservedAt.IsZero() {
			observedAt := metric.ObservedAt
			result.MetricsObservedAt = &observedAt
		}
		result.MetricsWindowSeconds = metric.Window.Seconds()
		result.MetricsSource = metric.Source
	}
	for _, status := range pod.Status.ContainerStatuses {
		result.Restarts += status.RestartCount
	}
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		item := ContainerSnapshot{
			Name:     container.Name,
			Requests: resourceList(container.Resources.Requests),
			Limits:   resourceList(container.Resources.Limits),
		}
		if status := findContainerStatus(pod, container.Name); status != nil && status.Resources != nil {
			actual := resourceList(status.Resources.Limits)
			item.ActualLimits = &actual
			item.ActualResourcesAvailable = true
		}
		result.Requests = add(result.Requests, item.Requests)
		result.Limits = add(result.Limits, item.Limits)
		if metric != nil {
			if value, ok := metric.Containers[container.Name]; ok {
				copy := value
				item.Usage = &copy
			}
		}
		result.Containers = append(result.Containers, item)
	}
	if metric != nil {
		total := Resources{}
		for _, usage := range metric.Containers {
			total = add(total, usage)
		}
		result.Usage = &total
	}
	result.SessionID = pod.Labels["bosun.io/session"]
	result.IsAgent = result.SessionID != "" && pod.Labels["app.kubernetes.io/managed-by"] == "bosun"
	if result.IsAgent && owners != nil {
		owner := owners[result.SessionID]
		result.Username = owner.Username
		result.SessionName = owner.SessionName
	}
	if result.IsAgent && scalingBySession != nil {
		if cr := scalingBySession[result.SessionID]; cr != nil {
			scaling := snapshotAgentScaling(cr, pod)
			result.ResourceScaling = &scaling
		}
	}
	return result
}

func snapshotAgentScaling(
	cr *bosunv1alpha1.AgentSession,
	pod *corev1.Pod,
) AgentResourceScalingSnapshot {
	result := AgentResourceScalingSnapshot{}
	if cr.Status.ResourceScaling != nil {
		result.LoadClass = string(cr.Status.ResourceScaling.LoadClass)
		result.RecommendedCPUMillicores = cr.Status.ResourceScaling.RecommendedCPUMillicores
		result.LastError = cr.Status.ResourceScaling.LastError
		if cr.Status.ResourceScaling.LastAppliedAt != nil {
			value := cr.Status.ResourceScaling.LastAppliedAt.Time
			result.LastAppliedAt = &value
		}
	}
	if condition := apimeta.FindStatusCondition(cr.Status.Conditions, "Ready"); condition != nil {
		result.WorkState = condition.Reason
	}
	if pod == nil {
		return result
	}
	if agent := findContainer(pod, agentContainerName); agent != nil {
		result.DesiredResources = resourceList(agent.Resources.Limits)
	}
	if status := findContainerStatus(pod, agentContainerName); status != nil && status.Resources != nil {
		actual := resourceList(status.Resources.Limits)
		result.ActualResources = &actual
		result.ActualResourcesAvailable = true
	}
	return result
}

func podResizeSnapshot(pod *corev1.Pod) *PodResizeSnapshot {
	for _, conditionType := range []corev1.PodConditionType{
		corev1.PodResizeInProgress,
		corev1.PodResizePending,
	} {
		for i := range pod.Status.Conditions {
			condition := &pod.Status.Conditions[i]
			if condition.Type == conditionType && condition.Status == corev1.ConditionTrue {
				return &PodResizeSnapshot{
					State:   string(condition.Type),
					Reason:  condition.Reason,
					Message: condition.Message,
				}
			}
		}
	}
	return nil
}

func findContainerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

func snapshotNode(node *corev1.Node, usage *Resources) NodeSnapshot {
	result := NodeSnapshot{
		Name:        node.Name,
		Status:      "NotReady",
		Kubelet:     node.Status.NodeInfo.KubeletVersion,
		Usage:       usage,
		Capacity:    resourceList(node.Status.Capacity),
		Allocatable: resourceList(node.Status.Allocatable),
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			result.Status = "Ready"
			break
		}
	}
	for label := range node.Labels {
		const prefix = "node-role.kubernetes.io/"
		if len(label) > len(prefix) && label[:len(prefix)] == prefix {
			result.Roles = append(result.Roles, label[len(prefix):])
		}
	}
	if role := node.Labels["role"]; role != "" {
		result.Roles = append(result.Roles, role)
	}
	sort.Strings(result.Roles)
	return result
}

func resourceList(list corev1.ResourceList) Resources {
	var result Resources
	if cpu, ok := list[corev1.ResourceCPU]; ok {
		result.CPUMillicores = cpu.MilliValue()
	}
	if memory, ok := list[corev1.ResourceMemory]; ok {
		result.MemoryBytes = memory.Value()
	}
	return result
}

func add(left, right Resources) Resources {
	return Resources{
		CPUMillicores: left.CPUMillicores + right.CPUMillicores,
		MemoryBytes:   left.MemoryBytes + right.MemoryBytes,
	}
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

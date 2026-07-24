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

	"github.com/Amsors/Bosun/backend/internal/session"
	"github.com/Amsors/Bosun/operator/pkg/sessionidentity"
)

const requestTimeout = 8 * time.Second

const agentContainerName = "agent"

var (
	ErrInvalidResize = errors.New("invalid agent resource limits")
	ErrNotRunning    = errors.New("agent Pod is not running")
)

type SessionStore interface {
	Get(context.Context, uuid.UUID, uuid.UUID) (session.Session, error)
}

type Service struct {
	sessions SessionStore
	owners   OwnerStore
	source   Source
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
	available := metricErr == nil
	if metricErr != nil && !metricsUnavailable(metricErr) {
		return SessionSnapshot{}, fmt.Errorf("get session Pod metrics: %w", metricErr)
	}
	var metricPtr *PodMetric
	if available {
		metricPtr = &metric
	}
	return SessionSnapshot{
		ObservedAt:       s.now(),
		MetricsAvailable: available,
		Pod:              snapshotPod(pod, metricPtr, nil),
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

	podMetrics, podMetricsErr := s.source.ListPodMetrics(ctx)
	if podMetricsErr != nil && !metricsUnavailable(podMetricsErr) {
		return ClusterSnapshot{}, fmt.Errorf("list Pod metrics: %w", podMetricsErr)
	}
	nodeMetrics, nodeMetricsErr := s.source.ListNodeMetrics(ctx)
	if nodeMetricsErr != nil && !metricsUnavailable(nodeMetricsErr) {
		return ClusterSnapshot{}, fmt.Errorf("list Node metrics: %w", nodeMetricsErr)
	}

	result := ClusterSnapshot{
		ObservedAt:           s.now(),
		PodMetricsAvailable:  podMetricsErr == nil,
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
		result.Pods = append(result.Pods, snapshotPod(&pods[i], metric, owners))
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

func (s *Service) ResizeAgent(
	ctx context.Context,
	sessionID uuid.UUID,
	request ResizeRequest,
) (SessionSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	pods, err := s.source.ListPods(ctx)
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("list Pods before resize: %w", err)
	}
	pod := agentPodForSession(pods, sessionID.String())
	if pod == nil {
		return SessionSnapshot{}, session.ErrNotFound
	}
	if pod.Status.Phase != corev1.PodRunning || !pod.DeletionTimestamp.IsZero() {
		return SessionSnapshot{}, ErrNotRunning
	}
	agent := findContainer(pod, agentContainerName)
	if agent == nil {
		return SessionSnapshot{}, session.ErrNotFound
	}
	limits := Resources(request)
	if err := validateResize(agent, limits); err != nil {
		return SessionSnapshot{}, err
	}
	updated, err := s.source.ResizePod(ctx, pod.Namespace, pod.Name, agentContainerName, limits)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return SessionSnapshot{}, session.ErrNotFound
		}
		if apierrors.IsInvalid(err) {
			return SessionSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidResize, err)
		}
		return SessionSnapshot{}, fmt.Errorf("resize agent Pod: %w", err)
	}
	metric, metricErr := s.source.GetPodMetric(ctx, updated.Namespace, updated.Name)
	available := metricErr == nil
	var metricPtr *PodMetric
	if available {
		metricPtr = &metric
	}
	return SessionSnapshot{
		ObservedAt:       s.now(),
		MetricsAvailable: available,
		Pod:              snapshotPod(updated, metricPtr, nil),
	}, nil
}

func snapshotPod(pod *corev1.Pod, metric *PodMetric, owners map[string]AgentOwner) PodSnapshot {
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
	return result
}

func agentPodForSession(pods []corev1.Pod, sessionID string) *corev1.Pod {
	for i := range pods {
		pod := &pods[i]
		if pod.Labels["bosun.io/session"] == sessionID &&
			pod.Labels["app.kubernetes.io/managed-by"] == "bosun" {
			return pod
		}
	}
	return nil
}

func validateResize(container *corev1.Container, limits Resources) error {
	if limits.CPUMillicores <= 0 || limits.MemoryBytes <= 0 {
		return ErrInvalidResize
	}
	requests := resourceList(container.Resources.Requests)
	if limits.CPUMillicores < requests.CPUMillicores ||
		limits.MemoryBytes < requests.MemoryBytes {
		return fmt.Errorf("%w: limits must be greater than or equal to requests", ErrInvalidResize)
	}
	return nil
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

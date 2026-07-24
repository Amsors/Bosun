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

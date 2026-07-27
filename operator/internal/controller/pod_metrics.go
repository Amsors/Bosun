package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	minimumKubeletSampleInterval = 500 * time.Millisecond
	maximumKubeletSampleInterval = 2 * time.Minute
)

// AgentPodMetric is one CPU observation derived from consecutive Kubelet counters.
type AgentPodMetric struct {
	Timestamp          time.Time
	Window             time.Duration
	CPUUsageMillicores int64
}

// PodMetricsReader reads CPU observations for Agent containers.
type PodMetricsReader interface {
	BeginCycle()
	GetAgentPodMetric(context.Context, *corev1.Pod) (AgentPodMetric, bool, error)
}

type kubeletContainerCounter struct {
	observedAt time.Time
	usage      uint64
}

type kubeletCPUState struct {
	counter kubeletContainerCounter
}

// kubeletPodMetricsReader reads one Summary snapshot per Node during each
// autoscaler cycle and keeps the previous counter for each Agent container.
type kubeletPodMetricsReader struct {
	client kubernetes.Interface

	mu            sync.Mutex
	summaries     map[string]map[string]kubeletContainerCounter
	summaryErrors map[string]error
	states        map[types.UID]kubeletCPUState
	seen          map[types.UID]struct{}
}

// NewKubeletPodMetricsReader creates a reader backed by the Kubelet Summary API.
func NewKubeletPodMetricsReader(coreClient kubernetes.Interface) PodMetricsReader {
	return &kubeletPodMetricsReader{
		client:        coreClient,
		summaries:     map[string]map[string]kubeletContainerCounter{},
		summaryErrors: map[string]error{},
		states:        map[types.UID]kubeletCPUState{},
		seen:          map[types.UID]struct{}{},
	}
}

// BeginCycle drops Node snapshots from the previous scan and prunes counters
// for containers that disappeared before the previous scan.
func (r *kubeletPodMetricsReader) BeginCycle() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for identity := range r.states {
		if _, ok := r.seen[identity]; !ok {
			delete(r.states, identity)
		}
	}
	r.seen = map[types.UID]struct{}{}
	r.summaries = map[string]map[string]kubeletContainerCounter{}
	r.summaryErrors = map[string]error{}
}

func (r *kubeletPodMetricsReader) GetAgentPodMetric(
	ctx context.Context,
	pod *corev1.Pod,
) (AgentPodMetric, bool, error) {
	if pod == nil || pod.Spec.NodeName == "" {
		return AgentPodMetric{}, false, fmt.Errorf("agent Pod has no assigned Node")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return AgentPodMetric{}, false, fmt.Errorf("kubelet metrics reader has no Kubernetes client")
	}
	identity := agentMetricIdentity(pod)
	r.seen[identity] = struct{}{}

	if err, failed := r.summaryErrors[pod.Spec.NodeName]; failed {
		return AgentPodMetric{}, false, err
	}
	summary, ok := r.summaries[pod.Spec.NodeName]
	if !ok {
		var err error
		summary, err = r.readNodeSummary(ctx, pod.Spec.NodeName)
		if err != nil {
			r.summaryErrors[pod.Spec.NodeName] = err
			return AgentPodMetric{}, false, err
		}
		r.summaries[pod.Spec.NodeName] = summary
	}
	counter, ok := summary[pod.Namespace+"/"+pod.Name]
	if !ok {
		return AgentPodMetric{}, false, fmt.Errorf(
			"kubelet Summary has no agent container for Pod %s/%s",
			pod.Namespace,
			pod.Name,
		)
	}

	state := r.states[identity]
	if state.counter.observedAt.IsZero() {
		r.states[identity] = kubeletCPUState{counter: counter}
		return AgentPodMetric{}, false, nil
	}
	if !counter.observedAt.After(state.counter.observedAt) {
		return AgentPodMetric{}, false, nil
	}

	elapsed := counter.observedAt.Sub(state.counter.observedAt)
	if counter.usage < state.counter.usage ||
		elapsed < minimumKubeletSampleInterval ||
		elapsed > maximumKubeletSampleInterval {
		r.states[identity] = kubeletCPUState{counter: counter}
		return AgentPodMetric{}, false, nil
	}

	metric := AgentPodMetric{
		Timestamp: counter.observedAt,
		Window:    elapsed,
		CPUUsageMillicores: max(0, int64(math.Round(
			float64(counter.usage-state.counter.usage)/
				float64(elapsed.Nanoseconds())*1000,
		))),
	}
	r.states[identity] = kubeletCPUState{counter: counter}
	return metric, true, nil
}

func (r *kubeletPodMetricsReader) readNodeSummary(
	ctx context.Context,
	nodeName string,
) (map[string]kubeletContainerCounter, error) {
	raw, err := r.client.CoreV1().RESTClient().Get().
		Resource("nodes").
		Name(nodeName).
		SubResource("proxy").
		Suffix("stats", "summary").
		Do(ctx).
		Raw()
	if err != nil {
		return nil, fmt.Errorf("read Kubelet Summary for Node %s: %w", nodeName, err)
	}
	counters, err := agentCountersFromSummary(raw)
	if err != nil {
		return nil, fmt.Errorf("decode Kubelet Summary for Node %s: %w", nodeName, err)
	}
	return counters, nil
}

type kubeletSummary struct {
	Pods []struct {
		PodRef struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"podRef"`
		Containers []struct {
			Name string `json:"name"`
			CPU  *struct {
				Time                 string  `json:"time"`
				UsageCoreNanoSeconds *uint64 `json:"usageCoreNanoSeconds"`
			} `json:"cpu"`
		} `json:"containers"`
	} `json:"pods"`
}

func agentCountersFromSummary(raw []byte) (map[string]kubeletContainerCounter, error) {
	var summary kubeletSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return nil, err
	}

	result := make(map[string]kubeletContainerCounter, len(summary.Pods))
	for i := range summary.Pods {
		pod := &summary.Pods[i]
		if pod.PodRef.Namespace == "" || pod.PodRef.Name == "" {
			continue
		}
		for j := range pod.Containers {
			container := &pod.Containers[j]
			if container.Name != agentContainerName ||
				container.CPU == nil ||
				container.CPU.UsageCoreNanoSeconds == nil {
				continue
			}
			observedAt, err := time.Parse(time.RFC3339Nano, container.CPU.Time)
			if err != nil || observedAt.IsZero() {
				continue
			}
			result[pod.PodRef.Namespace+"/"+pod.PodRef.Name] = kubeletContainerCounter{
				observedAt: observedAt.UTC(),
				usage:      *container.CPU.UsageCoreNanoSeconds,
			}
			break
		}
	}
	return result, nil
}

func agentMetricIdentity(pod *corev1.Pod) types.UID {
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]
		if status.Name == agentContainerName {
			return types.UID(fmt.Sprintf(
				"%s/%d/%s",
				pod.UID,
				status.RestartCount,
				status.ContainerID,
			))
		}
	}
	return pod.UID
}

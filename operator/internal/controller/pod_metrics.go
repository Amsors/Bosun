package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var podMetricsResource = schema.GroupVersionResource{
	Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods",
}

// AgentPodMetric is the CPU portion of one metrics.k8s.io PodMetrics object.
type AgentPodMetric struct {
	Timestamp          time.Time
	Window             time.Duration
	CPUUsageMillicores int64
}

// PodMetricsReader reads metrics for the Agent container.
type PodMetricsReader interface {
	GetAgentPodMetric(context.Context, string, string) (AgentPodMetric, error)
}

type kubernetesPodMetricsReader struct {
	client dynamic.Interface
}

// NewPodMetricsReader creates the production metrics.k8s.io reader.
func NewPodMetricsReader(dynamicClient dynamic.Interface) PodMetricsReader {
	return &kubernetesPodMetricsReader{client: dynamicClient}
}

func (r *kubernetesPodMetricsReader) GetAgentPodMetric(
	ctx context.Context,
	namespace, name string,
) (AgentPodMetric, error) {
	item, err := r.client.Resource(podMetricsResource).Namespace(namespace).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return AgentPodMetric{}, err
	}
	rawTimestamp, found, err := unstructured.NestedString(item.Object, "timestamp")
	if err != nil || !found || rawTimestamp == "" {
		return AgentPodMetric{}, fmt.Errorf("PodMetrics %s/%s has no timestamp", namespace, name)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, rawTimestamp)
	if err != nil {
		return AgentPodMetric{}, fmt.Errorf(
			"parse PodMetrics %s/%s timestamp: %w", namespace, name, err,
		)
	}
	var window time.Duration
	if rawWindow, _, nestedErr := unstructured.NestedString(item.Object, "window"); nestedErr != nil {
		return AgentPodMetric{}, fmt.Errorf(
			"decode PodMetrics %s/%s window: %w", namespace, name, nestedErr,
		)
	} else if rawWindow != "" {
		window, err = time.ParseDuration(rawWindow)
		if err != nil {
			return AgentPodMetric{}, fmt.Errorf(
				"parse PodMetrics %s/%s window: %w", namespace, name, err,
			)
		}
	}
	containers, found, err := unstructured.NestedSlice(item.Object, "containers")
	if err != nil || !found {
		return AgentPodMetric{}, fmt.Errorf("PodMetrics %s/%s has no containers", namespace, name)
	}
	for i := range containers {
		container, ok := containers[i].(map[string]any)
		if !ok {
			return AgentPodMetric{}, fmt.Errorf(
				"decode PodMetrics %s/%s container", namespace, name,
			)
		}
		containerName, _, _ := unstructured.NestedString(container, "name")
		if containerName != agentContainerName {
			continue
		}
		usage, found, err := unstructured.NestedStringMap(container, "usage")
		if err != nil || !found || usage[string(corev1.ResourceCPU)] == "" {
			return AgentPodMetric{}, fmt.Errorf(
				"PodMetrics %s/%s agent container has no CPU usage", namespace, name,
			)
		}
		cpu, err := resource.ParseQuantity(usage[string(corev1.ResourceCPU)])
		if err != nil {
			return AgentPodMetric{}, fmt.Errorf(
				"parse PodMetrics %s/%s agent CPU usage: %w", namespace, name, err,
			)
		}
		return AgentPodMetric{
			Timestamp: timestamp, Window: window, CPUUsageMillicores: cpu.MilliValue(),
		}, nil
	}
	return AgentPodMetric{}, fmt.Errorf(
		"PodMetrics %s/%s has no agent container", namespace, name,
	)
}

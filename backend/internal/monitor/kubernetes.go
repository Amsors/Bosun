package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

var (
	podMetricsResource  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	nodeMetricsResource = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
)

type PodMetric struct {
	ObservedAt time.Time
	Window     time.Duration
	Source     string
	Containers map[string]Resources
}

type ContainerCounter struct {
	CPUUsageSeconds       float64
	MemoryWorkingSetBytes int64
	ObservedAt            time.Time
}

type PodCounterMetric struct {
	ObservedAt time.Time
	Containers map[string]ContainerCounter
}

type Source interface {
	GetPod(context.Context, string, string) (*corev1.Pod, error)
	ListPods(context.Context) ([]corev1.Pod, error)
	ListNodes(context.Context) ([]corev1.Node, error)
	GetPodMetric(context.Context, string, string) (PodMetric, error)
	ListPodMetrics(context.Context) (map[string]PodMetric, error)
	ListNodeMetrics(context.Context) (map[string]Resources, error)
	GetNodePodCounters(context.Context, string) (map[string]PodCounterMetric, error)
	GetAgentSession(context.Context, string, string) (*bosunv1alpha1.AgentSession, error)
	ListAgentSessions(context.Context) ([]bosunv1alpha1.AgentSession, error)
}

type KubernetesSource struct {
	core    kubernetes.Interface
	dynamic dynamic.Interface
	objects client.Client
}

func NewKubernetesSource(cfg *rest.Config) (*KubernetesSource, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kubernetes REST config is required")
	}
	coreClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create core Kubernetes client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create metrics Kubernetes client: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register AgentSession scheme: %w", err)
	}
	objectClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create AgentSession Kubernetes client: %w", err)
	}
	return &KubernetesSource{core: coreClient, dynamic: dynamicClient, objects: objectClient}, nil
}

func (s *KubernetesSource) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	return s.core.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (s *KubernetesSource) ListPods(ctx context.Context) ([]corev1.Pod, error) {
	list, err := s.core.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (s *KubernetesSource) GetAgentSession(
	ctx context.Context,
	namespace, name string,
) (*bosunv1alpha1.AgentSession, error) {
	var session bosunv1alpha1.AgentSession
	if err := s.objects.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *KubernetesSource) ListAgentSessions(ctx context.Context) ([]bosunv1alpha1.AgentSession, error) {
	var sessions bosunv1alpha1.AgentSessionList
	if err := s.objects.List(ctx, &sessions); err != nil {
		return nil, err
	}
	return sessions.Items, nil
}

func (s *KubernetesSource) ListNodes(ctx context.Context) ([]corev1.Node, error) {
	list, err := s.core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (s *KubernetesSource) GetPodMetric(ctx context.Context, namespace, name string) (PodMetric, error) {
	item, err := s.dynamic.Resource(podMetricsResource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return PodMetric{}, err
	}
	return podMetricFromUnstructured(item)
}

func (s *KubernetesSource) ListPodMetrics(ctx context.Context) (map[string]PodMetric, error) {
	list, err := s.dynamic.Resource(podMetricsResource).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := make(map[string]PodMetric, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		metric, err := podMetricFromUnstructured(item)
		if err != nil {
			return nil, err
		}
		result[item.GetNamespace()+"/"+item.GetName()] = metric
	}
	return result, nil
}

func (s *KubernetesSource) ListNodeMetrics(ctx context.Context) (map[string]Resources, error) {
	list, err := s.dynamic.Resource(nodeMetricsResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := make(map[string]Resources, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		usage, found, err := unstructured.NestedStringMap(item.Object, "usage")
		if err != nil {
			return nil, fmt.Errorf("decode NodeMetrics %s usage: %w", item.GetName(), err)
		}
		if !found {
			continue
		}
		resources, err := parseUsage(usage)
		if err != nil {
			return nil, fmt.Errorf("decode NodeMetrics %s: %w", item.GetName(), err)
		}
		result[item.GetName()] = resources
	}
	return result, nil
}

func (s *KubernetesSource) GetNodePodCounters(
	ctx context.Context,
	nodeName string,
) (map[string]PodCounterMetric, error) {
	raw, err := s.core.CoreV1().RESTClient().Get().
		Resource("nodes").
		Name(nodeName).
		SubResource("proxy").
		Suffix("stats", "summary").
		Do(ctx).
		Raw()
	if err != nil {
		return nil, err
	}
	return podCountersFromSummary(raw)
}

func podMetricFromUnstructured(item *unstructured.Unstructured) (PodMetric, error) {
	containers, found, err := unstructured.NestedSlice(item.Object, "containers")
	if err != nil {
		return PodMetric{}, fmt.Errorf("decode PodMetrics %s/%s containers: %w", item.GetNamespace(), item.GetName(), err)
	}
	if !found {
		return PodMetric{
			ObservedAt: metricTimestamp(item),
			Window:     metricWindow(item),
			Source:     "metrics-server",
			Containers: map[string]Resources{},
		}, nil
	}
	result := PodMetric{
		ObservedAt: metricTimestamp(item),
		Window:     metricWindow(item),
		Source:     "metrics-server",
		Containers: make(map[string]Resources, len(containers)),
	}
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			return PodMetric{}, fmt.Errorf("decode PodMetrics %s/%s container", item.GetNamespace(), item.GetName())
		}
		name, _, _ := unstructured.NestedString(container, "name")
		usage, found, err := unstructured.NestedStringMap(container, "usage")
		if err != nil || !found {
			return PodMetric{}, fmt.Errorf("decode PodMetrics %s/%s container %s usage", item.GetNamespace(), item.GetName(), name)
		}
		value, err := parseUsage(usage)
		if err != nil {
			return PodMetric{}, fmt.Errorf("decode PodMetrics %s/%s container %s: %w", item.GetNamespace(), item.GetName(), name, err)
		}
		result.Containers[name] = value
	}
	return result, nil
}

func metricTimestamp(item *unstructured.Unstructured) time.Time {
	raw, _, _ := unstructured.NestedString(item.Object, "timestamp")
	timestamp, _ := time.Parse(time.RFC3339Nano, raw)
	return timestamp
}

func metricWindow(item *unstructured.Unstructured) time.Duration {
	raw, _, _ := unstructured.NestedString(item.Object, "window")
	window, _ := time.ParseDuration(raw)
	return window
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
			Memory *struct {
				WorkingSetBytes *uint64 `json:"workingSetBytes"`
			} `json:"memory"`
		} `json:"containers"`
	} `json:"pods"`
}

func podCountersFromSummary(raw []byte) (map[string]PodCounterMetric, error) {
	var summary kubeletSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return nil, fmt.Errorf("parse Kubelet stats summary: %w", err)
	}

	result := make(map[string]PodCounterMetric, len(summary.Pods))
	for _, pod := range summary.Pods {
		if pod.PodRef.Namespace == "" || pod.PodRef.Name == "" {
			continue
		}
		podMetric := PodCounterMetric{
			Containers: make(map[string]ContainerCounter, len(pod.Containers)),
		}
		for _, container := range pod.Containers {
			if container.Name == "" || container.CPU == nil ||
				container.CPU.UsageCoreNanoSeconds == nil {
				continue
			}
			observedAt, err := time.Parse(time.RFC3339Nano, container.CPU.Time)
			if err != nil || observedAt.IsZero() {
				continue
			}
			counter := ContainerCounter{
				CPUUsageSeconds: float64(*container.CPU.UsageCoreNanoSeconds) /
					float64(time.Second),
				ObservedAt: observedAt.UTC(),
			}
			if container.Memory != nil && container.Memory.WorkingSetBytes != nil {
				counter.MemoryWorkingSetBytes = int64(*container.Memory.WorkingSetBytes)
			}
			podMetric.Containers[container.Name] = counter
			if podMetric.ObservedAt.IsZero() || observedAt.Before(podMetric.ObservedAt) {
				podMetric.ObservedAt = observedAt.UTC()
			}
		}
		if len(podMetric.Containers) > 0 {
			result[pod.PodRef.Namespace+"/"+pod.PodRef.Name] = podMetric
		}
	}
	return result, nil
}

func parseUsage(usage map[string]string) (Resources, error) {
	var result Resources
	if raw := usage[string(corev1.ResourceCPU)]; raw != "" {
		value, err := resource.ParseQuantity(raw)
		if err != nil {
			return Resources{}, fmt.Errorf("parse CPU quantity: %w", err)
		}
		result.CPUMillicores = value.MilliValue()
	}
	if raw := usage[string(corev1.ResourceMemory)]; raw != "" {
		value, err := resource.ParseQuantity(raw)
		if err != nil {
			return Resources{}, fmt.Errorf("parse memory quantity: %w", err)
		}
		result.MemoryBytes = value.Value()
	}
	return result, nil
}

func metricsUnavailable(err error) bool {
	return apierrors.IsNotFound(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsForbidden(err)
}

func findContainer(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

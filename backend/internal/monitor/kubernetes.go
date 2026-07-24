package monitor

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	podMetricsResource  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	nodeMetricsResource = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
)

type PodMetric struct {
	Containers map[string]Resources
}

type Source interface {
	GetPod(context.Context, string, string) (*corev1.Pod, error)
	ListPods(context.Context) ([]corev1.Pod, error)
	ListNodes(context.Context) ([]corev1.Node, error)
	GetPodMetric(context.Context, string, string) (PodMetric, error)
	ListPodMetrics(context.Context) (map[string]PodMetric, error)
	ListNodeMetrics(context.Context) (map[string]Resources, error)
}

type KubernetesSource struct {
	core    kubernetes.Interface
	dynamic dynamic.Interface
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
	return &KubernetesSource{core: coreClient, dynamic: dynamicClient}, nil
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

func podMetricFromUnstructured(item *unstructured.Unstructured) (PodMetric, error) {
	containers, found, err := unstructured.NestedSlice(item.Object, "containers")
	if err != nil {
		return PodMetric{}, fmt.Errorf("decode PodMetrics %s/%s containers: %w", item.GetNamespace(), item.GetName(), err)
	}
	if !found {
		return PodMetric{Containers: map[string]Resources{}}, nil
	}
	result := PodMetric{Containers: make(map[string]Resources, len(containers))}
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

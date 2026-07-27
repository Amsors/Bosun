// Package resourcepolicy defines the shared Agent resource policy.
package resourcepolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ResourcePolicy contains immutable requests, defaults and hard limits.
type ResourcePolicy struct {
	CPURequestMillicores  int64
	MemoryRequestBytes    int64
	DefaultCPULimit       int64
	MinCPULimit           int64
	MaxCPULimit           int64
	MinMemoryRequestBytes int64
	MaxMemoryRequestBytes int64
}

var policy = ResourcePolicy{
	CPURequestMillicores:  500,
	MemoryRequestBytes:    mustBytes("2Gi"),
	DefaultCPULimit:       500,
	MinCPULimit:           500,
	MaxCPULimit:           3000,
	MinMemoryRequestBytes: mustBytes("1Gi"),
	MaxMemoryRequestBytes: mustBytes("64Gi"),
}

// Policy returns the resource policy shared by every AgentSession.
func Policy() ResourcePolicy {
	return policy
}

// ResourceRequirements returns the fixed requests and initial/default limits.
func ResourceRequirements(memoryBytes int64) (corev1.ResourceList, corev1.ResourceList) {
	if memoryBytes <= 0 {
		memoryBytes = policy.MemoryRequestBytes
	}
	memory := *resource.NewQuantity(memoryBytes, resource.BinarySI)
	return corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(policy.CPURequestMillicores, resource.DecimalSI),
			corev1.ResourceMemory: memory,
		}, corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(policy.DefaultCPULimit, resource.DecimalSI),
			corev1.ResourceMemory: memory,
		}
}

func mustBytes(raw string) int64 {
	value := resource.MustParse(raw)
	return value.Value()
}

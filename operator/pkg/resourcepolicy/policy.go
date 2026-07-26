// Package resourcepolicy defines the shared Agent resource policy.
package resourcepolicy

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

const (
	CPUStepMillicores = int64(50)
)

// ResourcePolicy contains immutable requests, defaults and hard limits.
type ResourcePolicy struct {
	CPURequestMillicores    int64
	MemoryRequestBytes      int64
	DefaultCPULimit         int64
	DefaultMemoryLimitBytes int64
	MinCPULimit             int64
	MaxCPULimit             int64
	MinMemoryLimitBytes     int64
	MaxMemoryLimitBytes     int64
}

var policy = ResourcePolicy{
	CPURequestMillicores:    500,
	MemoryRequestBytes:      mustBytes("2Gi"),
	DefaultCPULimit:         500,
	DefaultMemoryLimitBytes: mustBytes("3Gi"),
	MinCPULimit:             500,
	MaxCPULimit:             3000,
	MinMemoryLimitBytes:     mustBytes("2Gi"),
	MaxMemoryLimitBytes:     mustBytes("6Gi"),
}

// Policy returns the resource policy shared by every AgentSession.
func Policy() ResourcePolicy {
	return policy
}

// ResourceRequirements returns the fixed requests and initial/default limits.
func ResourceRequirements() (corev1.ResourceList, corev1.ResourceList) {
	return corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(policy.CPURequestMillicores, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(policy.MemoryRequestBytes, resource.BinarySI),
		}, corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(policy.DefaultCPULimit, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(policy.DefaultMemoryLimitBytes, resource.BinarySI),
		}
}

// ValidateManualLimits verifies the shared hard bounds used by both backend and Operator.
func ValidateManualLimits(limits bosunv1alpha1.ResourceValues) error {
	if limits.CPUMillicores < policy.MinCPULimit || limits.CPUMillicores > policy.MaxCPULimit {
		return fmt.Errorf(
			"CPU limit %dm is outside bounds [%dm, %dm]",
			limits.CPUMillicores, policy.MinCPULimit, policy.MaxCPULimit,
		)
	}
	if limits.MemoryBytes < policy.MinMemoryLimitBytes || limits.MemoryBytes > policy.MaxMemoryLimitBytes {
		return fmt.Errorf(
			"memory limit %d is outside bounds [%d, %d]",
			limits.MemoryBytes, policy.MinMemoryLimitBytes, policy.MaxMemoryLimitBytes,
		)
	}
	return nil
}

func mustBytes(raw string) int64 {
	value := resource.MustParse(raw)
	return value.Value()
}

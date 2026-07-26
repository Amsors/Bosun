// Package resourcepolicy defines the shared Agent tier resource policy.
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

// TierPolicy contains immutable requests, defaults and hard limits for a tier.
type TierPolicy struct {
	CPURequestMillicores    int64
	MemoryRequestBytes      int64
	DefaultCPULimit         int64
	DefaultMemoryLimitBytes int64
	MinCPULimit             int64
	MaxCPULimit             int64
	MinMemoryLimitBytes     int64
	MaxMemoryLimitBytes     int64
}

var policies = map[bosunv1alpha1.SessionTier]TierPolicy{
	bosunv1alpha1.SessionTierSmall: {
		CPURequestMillicores:    240,
		MemoryRequestBytes:      mustBytes("496Mi"),
		DefaultCPULimit:         450,
		DefaultMemoryLimitBytes: mustBytes("960Mi"),
		MinCPULimit:             250,
		MaxCPULimit:             1500,
		MinMemoryLimitBytes:     mustBytes("512Mi"),
		MaxMemoryLimitBytes:     mustBytes("3Gi"),
	},
	bosunv1alpha1.SessionTierMedium: {
		CPURequestMillicores:    490,
		MemoryRequestBytes:      mustBytes("1008Mi"),
		DefaultCPULimit:         950,
		DefaultMemoryLimitBytes: mustBytes("1984Mi"),
		MinCPULimit:             500,
		MaxCPULimit:             3000,
		MinMemoryLimitBytes:     mustBytes("1024Mi"),
		MaxMemoryLimitBytes:     mustBytes("6Gi"),
	},
}

// ForTier returns the policy for a supported AgentSession tier.
func ForTier(tier bosunv1alpha1.SessionTier) (TierPolicy, error) {
	policy, ok := policies[tier]
	if !ok {
		return TierPolicy{}, fmt.Errorf("unsupported session tier %q", tier)
	}
	return policy, nil
}

// ResourceRequirements returns the fixed requests and initial/default limits.
func ResourceRequirements(tier bosunv1alpha1.SessionTier) (corev1.ResourceList, corev1.ResourceList, error) {
	policy, err := ForTier(tier)
	if err != nil {
		return nil, nil, err
	}
	return corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(policy.CPURequestMillicores, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(policy.MemoryRequestBytes, resource.BinarySI),
		}, corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(policy.DefaultCPULimit, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(policy.DefaultMemoryLimitBytes, resource.BinarySI),
		}, nil
}

// ValidateManualLimits verifies the tier hard bounds used by both backend and Operator.
func ValidateManualLimits(tier bosunv1alpha1.SessionTier, limits bosunv1alpha1.ResourceValues) error {
	policy, err := ForTier(tier)
	if err != nil {
		return err
	}
	if limits.CPUMillicores < policy.MinCPULimit || limits.CPUMillicores > policy.MaxCPULimit {
		return fmt.Errorf(
			"CPU limit %dm is outside tier %s bounds [%dm, %dm]",
			limits.CPUMillicores, tier, policy.MinCPULimit, policy.MaxCPULimit,
		)
	}
	if limits.MemoryBytes < policy.MinMemoryLimitBytes || limits.MemoryBytes > policy.MaxMemoryLimitBytes {
		return fmt.Errorf(
			"memory limit %d is outside tier %s bounds [%d, %d]",
			limits.MemoryBytes, tier, policy.MinMemoryLimitBytes, policy.MaxMemoryLimitBytes,
		)
	}
	return nil
}

func mustBytes(raw string) int64 {
	value := resource.MustParse(raw)
	return value.Value()
}

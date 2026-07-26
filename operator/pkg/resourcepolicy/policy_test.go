package resourcepolicy

import (
	"testing"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

func TestValidateManualLimitsUsesTierHardBounds(t *testing.T) {
	tests := []struct {
		name   string
		tier   bosunv1alpha1.SessionTier
		limits bosunv1alpha1.ResourceValues
		valid  bool
	}{
		{
			name: "small minimum", tier: bosunv1alpha1.SessionTierSmall,
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 250, MemoryBytes: 512 * 1024 * 1024},
			valid:  true,
		},
		{
			name: "medium maximum", tier: bosunv1alpha1.SessionTierMedium,
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 3000, MemoryBytes: 6 * 1024 * 1024 * 1024},
			valid:  true,
		},
		{
			name: "small CPU too high", tier: bosunv1alpha1.SessionTierSmall,
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 1501, MemoryBytes: 1024 * 1024 * 1024},
		},
		{
			name: "medium memory too low", tier: bosunv1alpha1.SessionTierMedium,
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 1000, MemoryBytes: 1023 * 1024 * 1024},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManualLimits(tt.tier, tt.limits)
			if (err == nil) != tt.valid {
				t.Fatalf("ValidateManualLimits() error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestResourceRequirementsMatchTierDefaults(t *testing.T) {
	requests, limits, err := ResourceRequirements(bosunv1alpha1.SessionTierSmall)
	if err != nil {
		t.Fatalf("ResourceRequirements() error = %v", err)
	}
	if requests.Cpu().MilliValue() != 240 || requests.Memory().Value() != 496*1024*1024 {
		t.Fatalf("requests = %#v", requests)
	}
	if limits.Cpu().MilliValue() != 450 || limits.Memory().Value() != 960*1024*1024 {
		t.Fatalf("limits = %#v", limits)
	}
}

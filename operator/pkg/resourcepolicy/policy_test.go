package resourcepolicy

import (
	"testing"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

func TestValidateManualLimitsUsesSharedHardBounds(t *testing.T) {
	tests := []struct {
		name   string
		limits bosunv1alpha1.ResourceValues
		valid  bool
	}{
		{
			name:   "minimum",
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 500, MemoryBytes: 2 * 1024 * 1024 * 1024},
			valid:  true,
		},
		{
			name:   "maximum",
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 3000, MemoryBytes: 6 * 1024 * 1024 * 1024},
			valid:  true,
		},
		{
			name:   "CPU too high",
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 3001, MemoryBytes: 3 * 1024 * 1024 * 1024},
		},
		{
			name:   "memory too low",
			limits: bosunv1alpha1.ResourceValues{CPUMillicores: 1000, MemoryBytes: 2*1024*1024*1024 - 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManualLimits(tt.limits)
			if (err == nil) != tt.valid {
				t.Fatalf("ValidateManualLimits() error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestResourceRequirementsMatchSharedDefaults(t *testing.T) {
	requests, limits := ResourceRequirements()
	if requests.Cpu().MilliValue() != 500 || requests.Memory().Value() != 2*1024*1024*1024 {
		t.Fatalf("requests = %#v", requests)
	}
	if limits.Cpu().MilliValue() != 500 || limits.Memory().Value() != 3*1024*1024*1024 {
		t.Fatalf("limits = %#v", limits)
	}
}

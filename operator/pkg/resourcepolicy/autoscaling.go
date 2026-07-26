package resourcepolicy

import (
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/types"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

const (
	MetricsMaxAge      = 45 * time.Second
	SampleCapacity     = 20
	WarmupSamples      = 3
	ScaleDownSamples   = 3
	ScaleUpThreshold   = int64(75)
	ScaleDownThreshold = int64(35)
)

// ResourceSample is one valid Agent CPU observation.
type ResourceSample struct {
	PodUID         types.UID
	ObservedAt     time.Time
	MetricWindow   time.Duration
	CPUUsage       int64
	ActualCPULimit int64
}

// SampleWindow keeps the bounded, in-memory observations for one AgentSession.
type SampleWindow struct {
	podUID     types.UID
	generation int64
	samples    []ResourceSample
}

// Prepare resets observations when the Pod or AgentSession generation changes.
func (w *SampleWindow) Prepare(podUID types.UID, generation int64) bool {
	if w.podUID == podUID && w.generation == generation {
		return false
	}
	w.podUID = podUID
	w.generation = generation
	w.samples = nil
	return true
}

// Reset clears all observations and identity tracking.
func (w *SampleWindow) Reset() {
	w.podUID = ""
	w.generation = 0
	w.samples = nil
}

// Add inserts a sample unless the Pod UID and metrics timestamp were already seen.
func (w *SampleWindow) Add(sample ResourceSample) bool {
	for i := range w.samples {
		if w.samples[i].PodUID == sample.PodUID &&
			w.samples[i].ObservedAt.Equal(sample.ObservedAt) {
			return false
		}
	}
	w.samples = append(w.samples, sample)
	slices.SortFunc(w.samples, func(left, right ResourceSample) int {
		return left.ObservedAt.Compare(right.ObservedAt)
	})
	if len(w.samples) > SampleCapacity {
		w.samples = append([]ResourceSample(nil), w.samples[len(w.samples)-SampleCapacity:]...)
	}
	return true
}

// Samples returns a copy of the current observations.
func (w *SampleWindow) Samples() []ResourceSample {
	return append([]ResourceSample(nil), w.samples...)
}

// Recommendation classifies recent CPU load and returns its candidate CPU limit.
// A zero target means the current samples do not recommend a limit change.
func Recommendation(
	samples []ResourceSample,
	currentLimit int64,
	policy TierPolicy,
) (bosunv1alpha1.ResourceLoadClass, int64) {
	if currentLimit <= 0 {
		return bosunv1alpha1.ResourceLoadClassUnknown, 0
	}
	for i := range samples {
		if samples[i].ActualCPULimit <= 0 {
			return bosunv1alpha1.ResourceLoadClassUnknown, 0
		}
	}
	if len(samples) < WarmupSamples {
		return bosunv1alpha1.ResourceLoadClassWarmingUp, 0
	}

	recentHigh := samples[len(samples)-WarmupSamples:]
	high := 0
	for i := range recentHigh {
		if utilizationAtLeast(recentHigh[i], ScaleUpThreshold) {
			high++
		}
	}
	if high >= 2 {
		return bosunv1alpha1.ResourceLoadClassCPUHigh, ScaleUpTarget(currentLimit, policy)
	}

	if len(samples) >= ScaleDownSamples {
		recentLow := samples[len(samples)-ScaleDownSamples:]
		low := true
		for i := range recentLow {
			if !utilizationBelow(recentLow[i], ScaleDownThreshold) {
				low = false
				break
			}
		}
		if low {
			return bosunv1alpha1.ResourceLoadClassCPULow, ScaleDownTarget(currentLimit, policy)
		}
	}
	return bosunv1alpha1.ResourceLoadClassStable, 0
}

// ScaleUpTarget applies the 1.50 maximum increase ratio and tier hard maximum.
func ScaleUpTarget(currentLimit int64, policy TierPolicy) int64 {
	target := currentLimit * 3 / 2
	target = min(target, policy.MaxCPULimit)
	return clampAndRoundUp(target, policy.MinCPULimit, policy.MaxCPULimit)
}

// ScaleDownTarget halves idle CPU capacity while respecting the tier hard minimum.
func ScaleDownTarget(currentLimit int64, policy TierPolicy) int64 {
	target := (currentLimit + 1) / 2
	target = max(target, policy.MinCPULimit)
	return clampAndRoundUp(target, policy.MinCPULimit, policy.MaxCPULimit)
}

func utilizationAtLeast(sample ResourceSample, percentage int64) bool {
	return sample.CPUUsage*100 >= sample.ActualCPULimit*percentage
}

func utilizationBelow(sample ResourceSample, percentage int64) bool {
	return sample.CPUUsage*100 < sample.ActualCPULimit*percentage
}

func clampAndRoundUp(value, minimum, maximum int64) int64 {
	if value < minimum {
		value = minimum
	}
	if value > maximum {
		value = maximum
	}
	value = ((value + CPUStepMillicores - 1) / CPUStepMillicores) * CPUStepMillicores
	if value > maximum {
		return maximum
	}
	return value
}

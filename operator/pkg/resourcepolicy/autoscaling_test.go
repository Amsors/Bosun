package resourcepolicy

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"
)

func TestSampleWindowDeduplicatesAndResetsIdentity(t *testing.T) {
	var window SampleWindow
	podA := types.UID("pod-a")
	podB := types.UID("pod-b")
	window.Prepare(podA, 1)
	sample := ResourceSample{
		PodUID: podA, ObservedAt: time.Unix(10, 0),
		CPUUsage: 100, ActualCPULimit: 450,
	}
	if !window.Add(sample) || window.Add(sample) {
		t.Fatal("sample timestamp was not deduplicated")
	}
	if !window.Prepare(podB, 1) || len(window.Samples()) != 0 {
		t.Fatal("Pod UID change did not clear the sample window")
	}
	window.Add(ResourceSample{
		PodUID: podB, ObservedAt: time.Unix(20, 0),
		CPUUsage: 100, ActualCPULimit: 450,
	})
	if !window.Prepare(podB, 2) || len(window.Samples()) != 0 {
		t.Fatal("AgentSession generation change did not clear the sample window")
	}
}

func TestRecommendationWarmsUpAndIgnoresSingleSpike(t *testing.T) {
	policy := Policy()
	samples := cpuSamples(500, 100, 100)
	class, target := Recommendation(samples, 500, policy)
	if class != bosunv1alpha1.ResourceLoadClassWarmingUp || target != 0 {
		t.Fatalf("recommendation = %s, %dm", class, target)
	}

	samples = cpuSamples(500, 400, 100, 100)
	class, target = Recommendation(samples, 500, policy)
	if class != bosunv1alpha1.ResourceLoadClassStable || target != 0 {
		t.Fatalf("single spike recommendation = %s, %dm", class, target)
	}
}

func TestRecommendationScalesUpAfterTwoOfThreeHighSamples(t *testing.T) {
	policy := Policy()
	class, target := Recommendation(cpuSamples(500, 400, 100, 400), 500, policy)
	if class != bosunv1alpha1.ResourceLoadClassCPUHigh || target != 1000 {
		t.Fatalf("recommendation = %s, %dm, want CPUHigh 1000m", class, target)
	}
}

func TestRecommendationRequiresThreeLowSamples(t *testing.T) {
	policy := Policy()
	class, target := Recommendation(cpuSamples(1000, 100, 100), 1000, policy)
	if class != bosunv1alpha1.ResourceLoadClassWarmingUp || target != 0 {
		t.Fatalf("two low samples = %s, %dm", class, target)
	}
	class, target = Recommendation(cpuSamples(1000, 100, 100, 100), 1000, policy)
	if class != bosunv1alpha1.ResourceLoadClassCPULow || target != 500 {
		t.Fatalf("three low samples = %s, %dm, want CPULow 500m", class, target)
	}
}

func TestScaleTargetsRespectSharedBoundsAtMillicorePrecision(t *testing.T) {
	policy := Policy()
	if got := ScaleUpTarget(2500, policy); got != 3000 {
		t.Fatalf("ScaleUpTarget() = %dm", got)
	}
	if got := ScaleDownTarget(700, policy); got != 500 {
		t.Fatalf("ScaleDownTarget() = %dm", got)
	}
	if got := ScaleDownTarget(1501, policy); got != 750 {
		t.Fatalf("ScaleDownTarget() = %dm, want 750m without step rounding", got)
	}
	if got := ScaleDownTarget(500, policy); got != 500 {
		t.Fatalf("ScaleDownTarget() = %dm, want minimum 500m", got)
	}
}

func TestScaleTargetsFollowRequiredSequences(t *testing.T) {
	policy := Policy()
	current := int64(500)
	for index, want := range []int64{1000, 2000, 3000} {
		current = ScaleUpTarget(current, policy)
		if current != want {
			t.Fatalf("scale-up step %d = %dm, want %dm", index, current, want)
		}
	}
	for index, want := range []int64{1500, 750, 500} {
		current = ScaleDownTarget(current, policy)
		if current != want {
			t.Fatalf("scale-down step %d = %dm, want %dm", index, current, want)
		}
	}
}

func cpuSamples(limit int64, usage ...int64) []ResourceSample {
	samples := make([]ResourceSample, len(usage))
	for i := range usage {
		samples[i] = ResourceSample{
			PodUID:         "pod",
			ObservedAt:     time.Unix(int64(i+1), 0),
			CPUUsage:       usage[i],
			ActualCPULimit: limit,
		}
	}
	return samples
}

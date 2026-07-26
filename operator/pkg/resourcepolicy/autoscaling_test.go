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
	policy, _ := ForTier(bosunv1alpha1.SessionTierSmall)
	samples := cpuSamples(450, 100, 100)
	class, target := Recommendation(samples, 450, policy)
	if class != bosunv1alpha1.ResourceLoadClassWarmingUp || target != 0 {
		t.Fatalf("recommendation = %s, %dm", class, target)
	}

	samples = cpuSamples(450, 400, 100, 100)
	class, target = Recommendation(samples, 450, policy)
	if class != bosunv1alpha1.ResourceLoadClassStable || target != 0 {
		t.Fatalf("single spike recommendation = %s, %dm", class, target)
	}
}

func TestRecommendationScalesUpAfterTwoOfThreeHighSamples(t *testing.T) {
	policy, _ := ForTier(bosunv1alpha1.SessionTierSmall)
	class, target := Recommendation(cpuSamples(450, 340, 100, 400), 450, policy)
	if class != bosunv1alpha1.ResourceLoadClassCPUHigh || target != 700 {
		t.Fatalf("recommendation = %s, %dm, want CPUHigh 700m", class, target)
	}
}

func TestRecommendationRequiresEightLowSamples(t *testing.T) {
	policy, _ := ForTier(bosunv1alpha1.SessionTierMedium)
	class, target := Recommendation(cpuSamples(950, 100, 100, 100, 100, 100, 100, 100), 950, policy)
	if class != bosunv1alpha1.ResourceLoadClassStable || target != 0 {
		t.Fatalf("seven low samples = %s, %dm", class, target)
	}
	class, target = Recommendation(cpuSamples(950, 100, 100, 100, 100, 100, 100, 100, 100), 950, policy)
	if class != bosunv1alpha1.ResourceLoadClassCPULow || target != 750 {
		t.Fatalf("eight low samples = %s, %dm, want CPULow 750m", class, target)
	}
}

func TestScaleTargetsRespectTierBoundsAndStep(t *testing.T) {
	small, _ := ForTier(bosunv1alpha1.SessionTierSmall)
	if got := ScaleUpTarget(1450, small); got != 1500 {
		t.Fatalf("ScaleUpTarget() = %dm", got)
	}
	if got := ScaleDownTarget(300, small); got != 250 {
		t.Fatalf("ScaleDownTarget() = %dm", got)
	}
	if got := ScaleDownTarget(450, small); got != 350 {
		t.Fatalf("ScaleDownTarget() = %dm, want rounded 350m", got)
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

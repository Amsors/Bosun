package monitor

import (
	"testing"
	"time"
)

func TestLiveMetricSamplerCalculatesCPUFromAdjacentCounters(t *testing.T) {
	var sampler liveMetricSampler
	start := time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)
	first := PodCounterMetric{
		ObservedAt: start,
		Containers: map[string]ContainerCounter{
			"agent": {CPUUsageSeconds: 10, MemoryWorkingSetBytes: 256 * 1024 * 1024},
		},
	}
	if _, ready := sampler.podMetric("demo/agent-1", first); ready {
		t.Fatal("first cumulative counter cannot produce a CPU rate")
	}

	second := PodCounterMetric{
		ObservedAt: start.Add(time.Second),
		Containers: map[string]ContainerCounter{
			"agent": {CPUUsageSeconds: 10.25, MemoryWorkingSetBytes: 300 * 1024 * 1024},
		},
	}
	metric, ready := sampler.podMetric("demo/agent-1", second)
	if !ready {
		t.Fatal("second cumulative counter should produce a live sample")
	}
	usage := metric.Containers["agent"]
	if usage.CPUMillicores != 250 || usage.MemoryBytes != 300*1024*1024 {
		t.Fatalf("usage = %#v", usage)
	}
	if metric.Source != "kubelet-summary" || !metric.ObservedAt.Equal(second.ObservedAt) {
		t.Fatalf("metric metadata = %#v", metric)
	}
}

func TestLiveMetricSamplerReusesLastRateWhenKubeletSampleHasNotAdvanced(t *testing.T) {
	var sampler liveMetricSampler
	start := time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)
	_, _ = sampler.podMetric("demo/agent-1", PodCounterMetric{
		ObservedAt: start,
		Containers: map[string]ContainerCounter{
			"agent": {
				CPUUsageSeconds:       10,
				MemoryWorkingSetBytes: 256,
				ObservedAt:            start,
			},
		},
	})
	metric, _ := sampler.podMetric("demo/agent-1", PodCounterMetric{
		ObservedAt: start.Add(time.Second),
		Containers: map[string]ContainerCounter{
			"agent": {
				CPUUsageSeconds:       10.2,
				MemoryWorkingSetBytes: 300,
				ObservedAt:            start.Add(time.Second),
			},
		},
	})
	duplicate, ready := sampler.podMetric("demo/agent-1", PodCounterMetric{
		ObservedAt: start.Add(3 * time.Second),
		Containers: map[string]ContainerCounter{
			"agent": {
				CPUUsageSeconds:       10.2,
				MemoryWorkingSetBytes: 301,
				ObservedAt:            start.Add(time.Second),
			},
		},
	})
	if !ready || duplicate.Containers["agent"].CPUMillicores != 200 {
		t.Fatalf("duplicate scrape replaced last rate: before=%#v after=%#v", metric, duplicate)
	}
	if duplicate.Containers["agent"].MemoryBytes != 300 {
		t.Fatalf("duplicate scrape mixed a newer memory value into the old sample: %#v", duplicate)
	}
	if !duplicate.ObservedAt.Equal(start.Add(time.Second)) {
		t.Fatalf("duplicate observedAt = %v", duplicate.ObservedAt)
	}
}

func TestLiveMetricSamplerReportsZeroWhenNewKubeletSampleIsIdle(t *testing.T) {
	var sampler liveMetricSampler
	start := time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)
	_, _ = sampler.podMetric("demo/agent-1", PodCounterMetric{
		ObservedAt: start,
		Containers: map[string]ContainerCounter{
			"agent": {
				CPUUsageSeconds: 10,
				ObservedAt:      start,
			},
		},
	})
	_, _ = sampler.podMetric("demo/agent-1", PodCounterMetric{
		ObservedAt: start.Add(time.Second),
		Containers: map[string]ContainerCounter{
			"agent": {
				CPUUsageSeconds: 10.2,
				ObservedAt:      start.Add(time.Second),
			},
		},
	})

	idle, ready := sampler.podMetric("demo/agent-1", PodCounterMetric{
		ObservedAt: start.Add(2 * time.Second),
		Containers: map[string]ContainerCounter{
			"agent": {
				CPUUsageSeconds: 10.2,
				ObservedAt:      start.Add(2 * time.Second),
			},
		},
	})
	if !ready {
		t.Fatal("new Kubelet sample should produce a live sample")
	}
	if idle.Containers["agent"].CPUMillicores != 0 {
		t.Fatalf("idle CPU = %dm, want 0m", idle.Containers["agent"].CPUMillicores)
	}
	if !idle.ObservedAt.Equal(start.Add(2 * time.Second)) {
		t.Fatalf("idle observedAt = %v", idle.ObservedAt)
	}
}

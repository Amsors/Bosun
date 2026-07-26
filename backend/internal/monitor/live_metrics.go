package monitor

import (
	"math"
	"sync"
	"time"
)

const (
	minimumLiveSampleInterval = 500 * time.Millisecond
	maximumLiveSampleInterval = 2 * time.Minute
)

type liveCPUState struct {
	counterSeconds  float64
	rawObservedAt   time.Time
	usage           int64
	memoryBytes     int64
	usageObservedAt time.Time
	ready           bool
}

type liveMetricSampler struct {
	mu     sync.Mutex
	states map[string]liveCPUState
}

func (s *liveMetricSampler) podMetric(
	podKey string,
	snapshot PodCounterMetric,
) (PodMetric, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = map[string]liveCPUState{}
	}

	result := PodMetric{
		ObservedAt: snapshot.ObservedAt,
		Window:     0,
		Source:     "kubelet-summary",
		Containers: make(map[string]Resources, len(snapshot.Containers)),
	}
	complete := len(snapshot.Containers) > 0
	for containerName, counter := range snapshot.Containers {
		stateKey := podKey + "/" + containerName
		state := s.states[stateKey]
		counterObservedAt := counter.ObservedAt
		if counterObservedAt.IsZero() {
			counterObservedAt = snapshot.ObservedAt
		}
		elapsed := counterObservedAt.Sub(state.rawObservedAt)

		switch {
		case state.rawObservedAt.IsZero(),
			counter.CPUUsageSeconds < state.counterSeconds,
			counterObservedAt.Before(state.rawObservedAt),
			elapsed > maximumLiveSampleInterval:
			state = liveCPUState{
				counterSeconds: counter.CPUUsageSeconds,
				rawObservedAt:  counterObservedAt,
			}
		case elapsed >= minimumLiveSampleInterval:
			cpuMillicores := int64(math.Round(
				(counter.CPUUsageSeconds - state.counterSeconds) / elapsed.Seconds() * 1000,
			))
			state = liveCPUState{
				counterSeconds:  counter.CPUUsageSeconds,
				rawObservedAt:   counterObservedAt,
				usage:           max(0, cpuMillicores),
				memoryBytes:     counter.MemoryWorkingSetBytes,
				usageObservedAt: counterObservedAt,
				ready:           true,
			}
		}

		s.states[stateKey] = state
		if !state.ready {
			complete = false
			continue
		}
		result.Containers[containerName] = Resources{
			CPUMillicores: state.usage,
			MemoryBytes:   state.memoryBytes,
		}
		if state.usageObservedAt.Before(result.ObservedAt) {
			result.ObservedAt = state.usageObservedAt
		}
	}
	return result, complete
}

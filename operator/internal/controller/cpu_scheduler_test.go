package controller

import "testing"

func TestMaxMinCPUUsesStableMillicoreRemainders(t *testing.T) {
	got := maxMinCPU(5, []cpuDemand{
		{key: "b", limit: 5},
		{key: "a", limit: 5},
	})
	if got["a"] != 3 || got["b"] != 2 {
		t.Fatalf("allocation = %#v, want a=3 b=2", got)
	}
}

func TestProportionalCPUUsesLargestRemainderAndExactTotal(t *testing.T) {
	got := proportionalCPU(501, map[string]int64{
		"a": 200,
		"b": 800,
	})
	if got["a"] != 100 || got["b"] != 401 {
		t.Fatalf("reclaim = %#v, want a=100 b=401", got)
	}
}

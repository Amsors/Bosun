package session

import "testing"

func TestPriorityClassName(t *testing.T) {
	tests := []struct {
		priority string
		want     string
	}{
		{priority: "low", want: "bosun-free"},
		{priority: "normal", want: "bosun-normal"},
		{priority: "high", want: "bosun-high"},
		{priority: "", want: "bosun-normal"},
	}
	for _, tt := range tests {
		t.Run(tt.priority, func(t *testing.T) {
			if got := priorityClassName(tt.priority); got != tt.want {
				t.Fatalf("priorityClassName(%q) = %q, want %q", tt.priority, got, tt.want)
			}
		})
	}
}

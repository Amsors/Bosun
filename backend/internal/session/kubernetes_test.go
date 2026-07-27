package session

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

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

func TestAgentSessionFromDomainCarriesMemoryRequest(t *testing.T) {
	sessionID, _ := uuid.NewV7()
	userID, _ := uuid.NewV7()
	nonce, _ := uuid.NewV7()
	rec := Session{
		ID: sessionID, UserID: userID, CRNamespace: "bosun-user", CRName: "agent-session",
		MemoryRequestBytes: 7 * 1024 * 1024 * 1024,
		DesiredState:       "Running", ResumeNonce: nonce, Runtime: "claude-code",
		Provider: Provider{Mode: "platform"}, StoragePolicy: "local",
		CreatedAt: time.Unix(1, 0),
	}
	cr := agentSessionFromDomain(rec)
	if got := cr.Spec.MemoryRequest.String(); got != "7Gi" {
		t.Fatalf("spec.memoryRequest = %q, want 7Gi", got)
	}
}

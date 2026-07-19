package terminal

import (
	"sync"

	"github.com/google/uuid"
)

type leaseClient interface {
	stop(int, string)
}

type leaseRegistry struct {
	mu      sync.Mutex
	clients map[uuid.UUID]leaseClient
}

func newLeaseRegistry() *leaseRegistry {
	return &leaseRegistry{clients: make(map[uuid.UUID]leaseClient)}
}

func (r *leaseRegistry) acquire(sessionID uuid.UUID, next leaseClient) func() {
	r.mu.Lock()
	previous := r.clients[sessionID]
	r.clients[sessionID] = next
	r.mu.Unlock()

	if previous != nil {
		previous.stop(closeReplaced, replacedReason)
	}
	return func() {
		r.mu.Lock()
		if r.clients[sessionID] == next {
			delete(r.clients, sessionID)
		}
		r.mu.Unlock()
	}
}

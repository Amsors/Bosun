// Package session implements the P0 session lifecycle business model.
package session

import (
	"errors"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const MaxSessionsPerUser int64 = 20

var (
	ErrNotFound          = errors.New("session not found")
	ErrInvalidTransition = errors.New("invalid session transition")
	ErrCapacity          = errors.New("active session capacity unavailable")
	ErrEnvironmentFailed = errors.New("user environment failed")
	ErrEnvironmentReady  = errors.New("user environment not ready")
	ErrValidation        = errors.New("invalid session request")
	ErrIdempotency       = errors.New("idempotency conflict")
	ErrConcurrentUpdate  = errors.New("session was concurrently updated")
)

type Provider struct {
	Mode         string     `json:"mode"`
	CredentialID *uuid.UUID `json:"credentialID,omitempty"`
}

type Session struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	Name              string
	Priority          string
	CRNamespace       string
	CRName            string
	Tier              string
	Runtime           string
	Provider          Provider
	StoragePolicy     string
	DesiredState      string
	ResumeNonce       uuid.UUID
	Phase             string
	PhaseReason       string
	Conditions        []metav1.Condition
	LastActiveAt      *time.Time
	CRResourceVersion int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
	Version           int64
}

type CreateRequest struct {
	Name          string          `json:"name"`
	Priority      string          `json:"priority"`
	Tier          string          `json:"tier"`
	Runtime       string          `json:"runtime"`
	Provider      ProviderRequest `json:"provider"`
	StoragePolicy string          `json:"storagePolicy"`
}

type ProviderRequest struct {
	Mode         string `json:"mode"`
	CredentialID string `json:"credentialID,omitempty"`
}

type Page struct {
	Items []Session
	Total int64
}

type IdempotencyInput struct {
	Key         string
	Method      string
	Path        string
	RequestHash []byte
	Status      int
	Body        []byte
	ExpiresAt   time.Time
}

type Event struct {
	ID         uuid.UUID
	SessionID  uuid.UUID
	Type       string
	Payload    []byte
	OccurredAt time.Time
}

type Projection struct {
	SessionID       uuid.UUID
	Phase           string
	PhaseReason     string
	Conditions      []metav1.Condition
	LastActiveAt    *time.Time
	ResourceVersion int64
	OccurredAt      time.Time
}

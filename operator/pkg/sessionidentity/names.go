// Package sessionidentity defines the stable Kubernetes names shared by the
// backend, operator and gateway identity checks.
package sessionidentity

import (
	"crypto/sha256"
	"encoding/hex"
)

const shortIDLength = 12

const (
	LastActiveAnnotation  = "bosun.io/last-active-at"
	ResumeNonceAnnotation = "bosun.io/resume-nonce"
)

// ShortID derives a stable, non-sequential DNS-safe identifier from a UUID.
func ShortID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:shortIDLength]
}

func CRName(sessionID string) string {
	return "sess-" + ShortID(sessionID)
}

func PodName(sessionID string) string {
	return "agent-" + ShortID(sessionID)
}

func PVCName(sessionID string) string {
	return "workspace-" + ShortID(sessionID)
}

func ServiceAccountName(sessionID string) string {
	return "bosun-session-" + ShortID(sessionID)
}

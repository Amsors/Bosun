package session

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DTO struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	DesiredState  string             `json:"desiredState"`
	Tier          string             `json:"tier"`
	Runtime       string             `json:"runtime"`
	Provider      ProviderDTO        `json:"provider"`
	StoragePolicy string             `json:"storagePolicy"`
	Phase         string             `json:"phase"`
	PhaseReason   string             `json:"phaseReason,omitempty"`
	LastActiveAt  string             `json:"lastActiveAt,omitempty"`
	Conditions    []metav1.Condition `json:"conditions"`
	CreatedAt     string             `json:"createdAt"`
}

type ProviderDTO struct {
	Mode         string `json:"mode"`
	CredentialID string `json:"credentialID,omitempty"`
}

func ToDTO(rec Session) DTO {
	return toDTO(rec)
}

func toDTO(rec Session) DTO {
	conditions := rec.Conditions
	if conditions == nil {
		conditions = make([]metav1.Condition, 0)
	}
	dto := DTO{
		ID: rec.ID.String(), Name: rec.Name, DesiredState: rec.DesiredState, Tier: rec.Tier, Runtime: rec.Runtime,
		Provider: ProviderDTO{Mode: rec.Provider.Mode}, StoragePolicy: rec.StoragePolicy,
		Phase: rec.Phase, PhaseReason: rec.PhaseReason, Conditions: conditions,
		CreatedAt: rec.CreatedAt.UTC().Format(time.RFC3339),
	}
	if rec.Provider.CredentialID != nil {
		dto.Provider.CredentialID = rec.Provider.CredentialID.String()
	}
	if rec.LastActiveAt != nil {
		dto.LastActiveAt = rec.LastActiveAt.UTC().Format(time.RFC3339)
	}
	return dto
}

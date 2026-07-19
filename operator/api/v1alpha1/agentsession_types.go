/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type DesiredState string
type SessionTier string
type Runtime string
type ProviderMode string
type StoragePolicy string
type AgentSessionPhase string

const (
	DesiredStateRunning    DesiredState = "Running"
	DesiredStateHibernated DesiredState = "Hibernated"

	SessionTierSmall  SessionTier = "small"
	SessionTierMedium SessionTier = "medium"

	RuntimeClaudeCode Runtime = "claude-code"

	ProviderModePlatform ProviderMode = "platform"
	ProviderModeBYOK     ProviderMode = "byok"

	StoragePolicyLocal   StoragePolicy = "local"
	StoragePolicyArchive StoragePolicy = "archive"

	AgentSessionPhasePending      AgentSessionPhase = "Pending"
	AgentSessionPhaseProvisioning AgentSessionPhase = "Provisioning"
	AgentSessionPhaseRunning      AgentSessionPhase = "Running"
	AgentSessionPhaseIdle         AgentSessionPhase = "Idle"
	AgentSessionPhaseHibernating  AgentSessionPhase = "Hibernating"
	AgentSessionPhaseHibernated   AgentSessionPhase = "Hibernated"
	AgentSessionPhaseArchiving    AgentSessionPhase = "Archiving"
	AgentSessionPhaseArchived     AgentSessionPhase = "Archived"
	AgentSessionPhaseRestoring    AgentSessionPhase = "Restoring"
	AgentSessionPhaseDeleting     AgentSessionPhase = "Deleting"
	AgentSessionPhaseFailed       AgentSessionPhase = "Failed"
)

type ProviderSpec struct {
	// +kubebuilder:validation:Enum=platform;byok
	// +kubebuilder:default=platform
	Mode ProviderMode `json:"mode"`

	// CredentialID is a business identifier and never contains provider credentials.
	// +optional
	CredentialID string `json:"credentialID,omitempty"`
}

type AgentSessionSpec struct {
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	SessionID string `json:"sessionID"`

	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	UserID string `json:"userID"`

	// +kubebuilder:validation:Enum=Running;Hibernated
	// +kubebuilder:default=Running
	DesiredState DesiredState `json:"desiredState"`

	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	ResumeNonce string `json:"resumeNonce"`

	// +kubebuilder:validation:Enum=small;medium
	// +kubebuilder:default=small
	Tier SessionTier `json:"tier"`

	// +kubebuilder:validation:Enum=claude-code
	// +kubebuilder:default=claude-code
	Runtime Runtime `json:"runtime"`

	Provider ProviderSpec `json:"provider"`

	// +kubebuilder:validation:Enum=local;archive
	// +kubebuilder:default=local
	StoragePolicy StoragePolicy `json:"storagePolicy"`

	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=28800
	// +kubebuilder:default=1800
	IdleTimeoutSeconds int32 `json:"idleTimeoutSeconds"`

	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=28800
	// +kubebuilder:default=28800
	ActiveDeadlineSeconds int64 `json:"activeDeadlineSeconds"`

	// +kubebuilder:validation:Enum=bosun-free
	// +kubebuilder:default=bosun-free
	PriorityClassName string `json:"priorityClassName"`
}

type ArchiveStatus struct {
	ObjectKey string `json:"objectKey,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
}

type AgentSessionStatus struct {
	// +kubebuilder:validation:Enum=Pending;Provisioning;Running;Idle;Hibernating;Hibernated;Archiving;Archived;Restoring;Deleting;Failed
	Phase AgentSessionPhase `json:"phase,omitempty"`

	ObservedGeneration  int64         `json:"observedGeneration,omitempty"`
	ObservedResumeNonce string        `json:"observedResumeNonce,omitempty"`
	NodeName            string        `json:"nodeName,omitempty"`
	PodName             string        `json:"podName,omitempty"`
	PVCName             string        `json:"pvcName,omitempty"`
	LastActiveAt        *metav1.Time  `json:"lastActiveAt,omitempty"`
	Archive             ArchiveStatus `json:"archive,omitempty"`

	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Tier",type=string,JSONPath=`.spec.tier`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.nodeName`

type AgentSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSessionSpec   `json:"spec"`
	Status AgentSessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type AgentSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AgentSession{}, &AgentSessionList{})
		return nil
	})
}

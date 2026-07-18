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

type UserEnvironmentPhase string

const (
	UserEnvironmentPhasePending UserEnvironmentPhase = "Pending"
	UserEnvironmentPhaseReady   UserEnvironmentPhase = "Ready"
	UserEnvironmentPhaseFailed  UserEnvironmentPhase = "Failed"
)

type UserEnvironmentSpec struct {
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	UserID string `json:"userID"`

	// +kubebuilder:validation:Pattern=`^bosun-u-[a-z0-9]{8,16}$`
	Namespace string `json:"namespace"`

	// +kubebuilder:validation:Enum=mvp
	// +kubebuilder:default=mvp
	QuotaProfile string `json:"quotaProfile"`
}

type UserEnvironmentStatus struct {
	// +kubebuilder:validation:Enum=Pending;Ready;Failed
	Phase UserEnvironmentPhase `json:"phase,omitempty"`

	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ue
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`

type UserEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UserEnvironmentSpec   `json:"spec"`
	Status UserEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type UserEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UserEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &UserEnvironment{}, &UserEnvironmentList{})
		return nil
	})
}

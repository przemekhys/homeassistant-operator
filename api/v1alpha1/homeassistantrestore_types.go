/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SecretKeyReference identifies a key in a namespace-local Secret without
// placing credential material in the custom resource.
type SecretKeyReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// RestoreSource selects exactly one source already known to Home Assistant.
type RestoreSource struct {
	// +optional
	AgentID string `json:"agentID,omitempty"`
	// Uploaded means backupID identifies an archive previously uploaded through
	// Home Assistant's native endpoint.
	// +optional
	Uploaded *UploadedRestoreSource `json:"uploaded,omitempty"`
}

type UploadedRestoreSource struct{}

// RestoreScope is intentionally limited to complete Home Assistant restores.
type RestoreScope struct {
	HomeAssistant bool `json:"homeAssistant"`
	// +optional
	IncludeDatabase *bool `json:"includeDatabase,omitempty"`
}

// HomeAssistantRestoreSpec defines a destructive, one-shot native restore.
type HomeAssistantRestoreSpec struct {
	HomeAssistantRef HomeAssistantTargetReference `json:"homeAssistantRef"`
	// +kubebuilder:validation:MinLength=1
	BackupID                   string             `json:"backupID"`
	Source                     RestoreSource      `json:"source"`
	EncryptionKeySecretRef     SecretKeyReference `json:"encryptionKeySecretRef"`
	PostRestoreAccessSecretRef SecretKeyReference `json:"postRestoreAccessSecretRef"`
	Scope                      RestoreScope       `json:"scope"`
	// +kubebuilder:validation:Enum=CreateAndVerify;Skip
	SafetyBackupPolicy string `json:"safetyBackupPolicy"`
	// +kubebuilder:validation:Enum=KeepPaused;AutomaticAfterVerification
	// +kubebuilder:default=KeepPaused
	ResumePolicy string `json:"resumePolicy,omitempty"`
}

// RestoreOperationPhase projects the durable restore ledger.
// +kubebuilder:validation:Enum=Pending;Validating;AwaitingApproval;AcquiringPause;SafetyBackup;PreparingTarget;DispatchPrepared;Restoring;WaitingForHA;Verifying;CompletedPaused;CompletedResumed;Failed;OutcomeUnknown;Blocked
type RestoreOperationPhase string

const (
	RestorePhasePending          RestoreOperationPhase = "Pending"
	RestorePhaseValidating       RestoreOperationPhase = "Validating"
	RestorePhaseAwaitingApproval RestoreOperationPhase = "AwaitingApproval"
	RestorePhaseAcquiringPause   RestoreOperationPhase = "AcquiringPause"
	RestorePhaseSafetyBackup     RestoreOperationPhase = "SafetyBackup"
	RestorePhasePreparingTarget  RestoreOperationPhase = "PreparingTarget"
	RestorePhaseDispatchPrepared RestoreOperationPhase = "DispatchPrepared"
	RestorePhaseRestoring        RestoreOperationPhase = "Restoring"
	RestorePhaseWaitingForHA     RestoreOperationPhase = "WaitingForHA"
	RestorePhaseVerifying        RestoreOperationPhase = "Verifying"
	RestorePhaseCompletedPaused  RestoreOperationPhase = "CompletedPaused"
	RestorePhaseCompletedResumed RestoreOperationPhase = "CompletedResumed"
	RestorePhaseFailed           RestoreOperationPhase = "Failed"
	RestorePhaseOutcomeUnknown   RestoreOperationPhase = "OutcomeUnknown"
	RestorePhaseBlocked          RestoreOperationPhase = "Blocked"
)

type HomeAssistantRestoreStatus struct {
	// +optional
	Phase RestoreOperationPhase `json:"phase,omitempty"`
	// +optional
	EvidenceRef string `json:"evidenceRef,omitempty"`
	// +optional
	Challenge string `json:"challenge,omitempty"`
	// +optional
	PlanDigest string `json:"planDigest,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=harestore,categories=homeassistant
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backupID`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec == oldSelf.spec",message="spec is immutable after creation"
// +kubebuilder:validation:XValidation:rule="has(self.spec.source.agentID) != has(self.spec.source.uploaded)",message="exactly one of source.agentID or source.uploaded must be specified"
type HomeAssistantRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantRestoreSpec   `json:"spec,omitempty"`
	Status HomeAssistantRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type HomeAssistantRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantRestore{}, &HomeAssistantRestoreList{})
}

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

// HomeAssistantTargetReference binds an operation to one concrete target.
type HomeAssistantTargetReference struct {
	// Name is the namespace-local HomeAssistant name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// UID prevents a recreated target with the same name from accepting an old operation.
	// +kubebuilder:validation:MinLength=1
	UID string `json:"uid"`
}

// HomeAssistantBackupSpec defines one native backup request. It is immutable
// after creation so a completed request cannot be repurposed.
type HomeAssistantBackupSpec struct {
	HomeAssistantRef HomeAssistantTargetReference `json:"homeAssistantRef"`
	// Name is an optional Home Assistant archive name, not an idempotency key.
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	IncludeDatabase *bool `json:"includeDatabase,omitempty"`
	// AgentIDs is an optional non-empty set. Omission inherits the observed HA selection.
	// +optional
	// +listType=set
	AgentIDs []string `json:"agentIDs"`
}

// BackupOperationPhase is the durable projection of one native request.
// +kubebuilder:validation:Enum=Pending;Validating;WaitingForGate;DispatchPrepared;Running;Succeeded;PartiallySucceeded;Failed;OutcomeUnknown
type BackupOperationPhase string

const (
	BackupPhasePending            BackupOperationPhase = "Pending"
	BackupPhaseValidating         BackupOperationPhase = "Validating"
	BackupPhaseWaitingForGate     BackupOperationPhase = "WaitingForGate"
	BackupPhaseDispatchPrepared   BackupOperationPhase = "DispatchPrepared"
	BackupPhaseRunning            BackupOperationPhase = "Running"
	BackupPhaseSucceeded          BackupOperationPhase = "Succeeded"
	BackupPhasePartiallySucceeded BackupOperationPhase = "PartiallySucceeded"
	BackupPhaseFailed             BackupOperationPhase = "Failed"
	BackupPhaseOutcomeUnknown     BackupOperationPhase = "OutcomeUnknown"
)

// HomeAssistantBackupStatus contains only observable operation facts and
// references to durable evidence; it never contains passwords or tokens.
type HomeAssistantBackupStatus struct {
	// +optional
	Phase BackupOperationPhase `json:"phase,omitempty"`
	// +optional
	EvidenceRef string `json:"evidenceRef,omitempty"`
	// +optional
	BackupID string `json:"backupID,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=habackup,categories=homeassistant
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.homeAssistantRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec == oldSelf.spec",message="spec is immutable after creation"
type HomeAssistantBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HomeAssistantBackupSpec   `json:"spec,omitempty"`
	Status HomeAssistantBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type HomeAssistantBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HomeAssistantBackup{}, &HomeAssistantBackupList{})
}

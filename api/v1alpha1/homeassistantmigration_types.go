package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MigrationTarget identifies an immutable HomeAssistant target by name and UID.
type MigrationTarget struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
}

// MigrationApproval binds a user decision to the exact observed operation inputs.
type MigrationApproval struct {
	// +kubebuilder:validation:MaxLength=64
	ID string `json:"id"`
	// +kubebuilder:validation:MaxLength=128
	Digest string `json:"digest"`
	// +kubebuilder:validation:MaxLength=128
	TargetUID string `json:"targetUID,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	ArchiveDigest string `json:"archiveDigest,omitempty"`
	// VerificationDigest binds the approved non-secret verification set.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	VerificationDigest string `json:"verificationDigest,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	ScopeDigest string `json:"scopeDigest,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	SourceUID string `json:"sourceUID,omitempty"`
	Approved  bool   `json:"approved"`
}

// MigrationSourceWorkload identifies a source workload in the migration's
// namespace. The operator observes it but never modifies it.
type MigrationSourceWorkload struct {
	// +kubebuilder:validation:Enum=Deployment;StatefulSet
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
}

// MigrationArchive identifies an encrypted native backup without serializing
// its contents or recovery key.
type MigrationArchive struct {
	BackupID string `json:"backupID"`
	AgentID  string `json:"agentID"`
	Digest   string `json:"digest"`
}

// MigrationArchiveStorage names the independent archive volume and descriptor.
type MigrationArchiveStorage struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	PVCName string `json:"pvcName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DescriptorName string `json:"descriptorName"`
}

// MigrationVerificationItem identifies a non-secret datum to verify after restore.
type MigrationVerificationItem struct {
	// +kubebuilder:validation:Enum=EntityRegistryIdentity;IntegrationEntryIdentity;TodoItems
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	ID string `json:"id"`
	// ExpectedDigest identifies the approved non-secret data to compare.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	ExpectedDigest string `json:"expectedDigest"`
}

// MigrationResolution records an administrator's decision about an ambiguous request.
// +structType=atomic
// +kubebuilder:validation:XValidation:rule="self.decision == 'ObserveExternalResult' ? has(self.evidenceRef) : !has(self.evidenceRef)",message="only observation requires an evidence reference"
type MigrationResolution struct {
	// +kubebuilder:validation:Enum=v1
	Version string `json:"version"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	RequestID string `json:"requestID"`
	// +kubebuilder:validation:Enum=backup;upload;restore
	Stage string `json:"stage"`
	// +kubebuilder:validation:Enum=ObserveExternalResult;Abort
	Decision    string                      `json:"decision"`
	EvidenceRef *MigrationEvidenceReference `json:"evidenceRef,omitempty"`
}

// HomeAssistantMigrationSpec is durable intent for one migration. It never stores
// tokens, recovery keys, archive contents, or a decision only in status.
type HomeAssistantMigrationSpec struct {
	// ExecutionProtocol has no default: legacy operations cannot be upgraded implicitly.
	// +kubebuilder:validation:Enum=v1
	ExecutionProtocol string `json:"executionProtocol,omitempty"`
	// +listType=map
	// +listMapKey=stage
	// +kubebuilder:validation:MaxItems=3
	ExecutionRequests []MigrationExecutionRequest `json:"executionRequests,omitempty"`
	// +listType=map
	// +listMapKey=stage
	// +kubebuilder:validation:MaxItems=3
	ExecutionAttempts []MigrationExecutionAttempt `json:"executionAttempts,omitempty"`
	// ExecutionDispatches prevents a lost receipt from reopening a POST permit.
	// +listType=map
	// +listMapKey=stage
	// +kubebuilder:validation:MaxItems=3
	ExecutionDispatches []MigrationExecutionAttempt `json:"executionDispatches,omitempty"`
	// ProxyCredentialBinding pins operational TLS material without its values.
	// +optional
	ProxyCredentialBinding *MigrationProxyCredentialBinding `json:"proxyCredentialBinding,omitempty"`
	// +kubebuilder:validation:MaxLength=2048
	SourceID string `json:"sourceID"`
	// SourceEndpoint is the endpoint observed in the approved scan, without credentials.
	SourceEndpoint string          `json:"sourceEndpoint,omitempty"`
	Target         MigrationTarget `json:"target"`
	// +kubebuilder:validation:MaxLength=128
	ReportID string `json:"reportID,omitempty"`
	// BackupScopeDigest is the redacted scope observed in the approved report.
	// It must be rechecked against the source before any backup is submitted.
	// +optional
	BackupScopeDigest string `json:"backupScopeDigest,omitempty"`
	// SourceTokenSecretRef identifies the source API token without storing its
	// value in the migration operation. The Secret is in this namespace.
	// +optional
	SourceTokenSecretRef *corev1.SecretKeySelector `json:"sourceTokenSecretRef,omitempty"`
	// RecoveryKeySecretRef identifies the separately stored backup recovery key.
	// The value is never copied to the migration object or its status.
	// +optional
	RecoveryKeySecretRef *corev1.SecretKeySelector `json:"recoveryKeySecretRef,omitempty"`
	// SourceWorkload is optional for external sources. When present, it must be
	// stopped and have no running or terminating selected Pods before cutover.
	// +optional
	SourceWorkload *MigrationSourceWorkload `json:"sourceWorkload,omitempty"`
	// Archive and RestoreScopeDigest bind the destructive restore approval to
	// one exact archive and declared restore scope.
	// +optional
	Archive *MigrationArchive `json:"archive,omitempty"`
	// +optional
	RestoreScopeDigest string `json:"restoreScopeDigest,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	Approvals      []MigrationApproval      `json:"approvals,omitempty"`
	ArchiveStorage *MigrationArchiveStorage `json:"archiveStorage,omitempty"`
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=id
	// +kubebuilder:validation:MaxItems=256
	Verification []MigrationVerificationItem `json:"verification,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +listType=set
	Resolutions     []MigrationResolution `json:"resolutions,omitempty"`
	CancelRequested bool                  `json:"cancelRequested,omitempty"`
}

// HomeAssistantMigrationStatus contains only observations that can be rebuilt.
type HomeAssistantMigrationStatus struct {
	Phase string `json:"phase,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hamigration,categories=homeassistant
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.sourceID == oldSelf.spec.sourceID",message="spec.sourceID is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec.target == oldSelf.spec.target",message="spec.target is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || (has(self.spec.reportID) == has(oldSelf.spec.reportID) && (!has(self.spec.reportID) || self.spec.reportID == oldSelf.spec.reportID))",message="spec.reportID is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || (has(self.spec.backupScopeDigest) == has(oldSelf.spec.backupScopeDigest) && (!has(self.spec.backupScopeDigest) || self.spec.backupScopeDigest == oldSelf.spec.backupScopeDigest))",message="spec.backupScopeDigest is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.sourceEndpoint) || self.spec.sourceEndpoint == self.spec.sourceID",message="source endpoint must match the approved source identity"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || !has(oldSelf.spec.resolutions) || (has(self.spec.resolutions) && oldSelf.spec.resolutions.all(r, r in self.spec.resolutions))",message="migration resolutions are append-only"
// +kubebuilder:validation:XValidation:rule="has(self.spec.executionProtocol) == has(oldSelf.spec.executionProtocol) && (!has(self.spec.executionProtocol) || self.spec.executionProtocol == oldSelf.spec.executionProtocol)",message="execution protocol and its presence are immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.executionRequests) || (has(self.spec.executionRequests) && oldSelf.spec.executionRequests.all(r, self.spec.executionRequests.exists(n, n == r)))",message="execution requests are append-only"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.executionAttempts) || (has(self.spec.executionAttempts) && oldSelf.spec.executionAttempts.all(r, self.spec.executionAttempts.exists(n, n == r)))",message="execution attempts are append-only"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.executionDispatches) || (has(self.spec.executionDispatches) && oldSelf.spec.executionDispatches.all(r, self.spec.executionDispatches.exists(n, n == r)))",message="execution dispatches are append-only"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.proxyCredentialBinding) || (has(self.spec.proxyCredentialBinding) && self.spec.proxyCredentialBinding == oldSelf.spec.proxyCredentialBinding)",message="proxy credential binding is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.proxyCredentialBinding) || (has(self.spec.executionRequests) && self.spec.executionRequests.exists(r, r.stage == 'upload' && r.requestID == self.spec.proxyCredentialBinding.requestID))",message="proxy credential binding requires accepted upload"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.proxyCredentialBinding) || has(oldSelf.spec.proxyCredentialBinding) || !has(self.spec.cancelRequested) || !self.spec.cancelRequested",message="proxy credential binding cannot be added after cancellation"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.proxyCredentialBinding) || has(oldSelf.spec.proxyCredentialBinding) || !has(self.spec.resolutions) || !self.spec.resolutions.exists(r, r.decision == 'Abort')",message="proxy credential binding cannot be added after abort"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.executionRequests) || size(oldSelf.spec.executionRequests) == 0 || (has(self.spec.verification) && self.spec.verification == oldSelf.spec.verification)",message="accepted verification set is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || size(self.spec.executionRequests) == 0 || (has(self.spec.executionProtocol) && self.spec.executionProtocol == 'v1' && has(self.spec.verification) && size(self.spec.verification) > 0)",message="execution requires protocol and verification"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionAttempts) || self.spec.executionAttempts.all(a, has(self.spec.executionRequests) && self.spec.executionRequests.exists(r, r.stage == a.stage && r.requestID == a.requestID))",message="attempt must identify an accepted request"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionDispatches) || self.spec.executionDispatches.all(d, has(self.spec.executionAttempts) && self.spec.executionAttempts.exists(a, a.stage == d.stage && a.requestID == d.requestID))",message="dispatch must identify a started attempt"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionDispatches) || self.spec.executionDispatches.all(d, (has(oldSelf.spec.executionDispatches) && oldSelf.spec.executionDispatches.exists(o, o == d)) || !has(self.spec.cancelRequested) || !self.spec.cancelRequested)",message="new dispatches cannot be claimed after cancellation"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || self.spec.executionRequests.all(r, (has(oldSelf.spec.executionRequests) && oldSelf.spec.executionRequests.exists(o, o == r)) || ((!has(self.spec.cancelRequested) || !self.spec.cancelRequested) && r.sourceID == self.spec.sourceID && r.target.name == self.spec.target.name && r.target.uid == self.spec.target.uid && has(self.spec.reportID) && r.reportID == self.spec.reportID && has(self.spec.approvals) && self.spec.approvals.exists(a, a.id == (r.stage == 'backup' ? 'backup' : 'restore') && a.approved && has(a.targetUID) && a.targetUID == r.target.uid && has(a.scopeDigest) && a.scopeDigest == r.scopeDigest && has(a.verificationDigest) && a.verificationDigest == r.verificationDigest && (r.stage == 'backup' ? a.digest == r.reportID : (has(a.archiveDigest) && a.archiveDigest == r.archiveDigest)))))",message="new requests require current bound approval and must not be cancelled"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || self.spec.executionRequests.all(r, (has(oldSelf.spec.executionRequests) && oldSelf.spec.executionRequests.exists(o, o == r)) || (has(self.spec.approvals) && (r.stage == 'backup' ? self.spec.approvals.exists(a, a.id == 'backup-settings-stable' && a.approved && a.digest == r.reportID && has(a.scopeDigest) && a.scopeDigest == r.scopeDigest && has(a.targetUID) && a.targetUID == r.target.uid) : self.spec.approvals.exists(a, a.id == 'source-stop' && a.approved && a.digest == r.reportID && has(a.targetUID) && a.targetUID == r.target.uid))))",message="new requests require settings stability or source stop consent"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || self.spec.executionRequests.all(r, r.stage != 'restore' || self.spec.executionRequests.exists(u, u.stage == 'upload' && u.requestID == r.uploadRequestID && u.archiveRef == r.archiveRef && u.archiveDigest == r.archiveDigest && u.archivePVCUID == r.archivePVCUID && u.scopeDigest == r.scopeDigest))",message="restore must bind accepted upload"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || self.spec.executionRequests.all(r, (has(oldSelf.spec.executionRequests) && oldSelf.spec.executionRequests.exists(o, o == r)) || (r.stage == 'backup' ? (has(self.spec.sourceEndpoint) && r.sourceEndpoint == self.spec.sourceEndpoint && has(self.spec.backupScopeDigest) && r.scopeDigest == self.spec.backupScopeDigest && has(self.spec.archiveStorage) && r.archiveStorage.pvcName == self.spec.archiveStorage.pvcName && r.archiveStorage.descriptorName == self.spec.archiveStorage.descriptorName) : (has(self.spec.archive) && r.archiveDigest == self.spec.archive.digest && has(self.spec.restoreScopeDigest) && r.scopeDigest == self.spec.restoreScopeDigest)))",message="new request must bind current scope and archive inputs"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || self.spec.executionRequests.all(r, (has(oldSelf.spec.executionRequests) && oldSelf.spec.executionRequests.exists(o, o == r)) || (r.stage != 'backup' || (has(self.spec.sourceTokenSecretRef) && has(self.spec.recoveryKeySecretRef) && r.sourceToken.name == self.spec.sourceTokenSecretRef.name && r.sourceToken.key == self.spec.sourceTokenSecretRef.key && r.recoveryKey.name == self.spec.recoveryKeySecretRef.name && r.recoveryKey.key == self.spec.recoveryKeySecretRef.key)))",message="backup credentials must bind current secret references"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || self.spec.executionRequests.all(r, (has(oldSelf.spec.executionRequests) && oldSelf.spec.executionRequests.exists(o, o == r)) || (r.stage != 'restore' || (has(self.spec.recoveryKeySecretRef) && r.recoveryKey.name == self.spec.recoveryKeySecretRef.name && r.recoveryKey.key == self.spec.recoveryKeySecretRef.key)))",message="restore credentials must bind current secret reference"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionRequests) || self.spec.executionRequests.all(r, (has(oldSelf.spec.executionRequests) && oldSelf.spec.executionRequests.exists(o, o == r)) || (r.stage == 'backup' || (has(r.sourceWorkload) == has(self.spec.sourceWorkload) && (!has(r.sourceWorkload) || (r.sourceWorkload.uid == self.spec.sourceWorkload.uid && r.sourceWorkload.name == self.spec.sourceWorkload.name && r.sourceWorkload.kind == self.spec.sourceWorkload.kind && has(self.spec.approvals) && self.spec.approvals.exists(a, a.id == 'source-stop' && a.approved && has(a.sourceUID) && a.sourceUID == r.sourceWorkload.uid))))))",message="new restore stages must bind the source workload and stop approval"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.executionProtocol) || self.spec.executionProtocol != 'v1' || !has(self.spec.resolutions) || self.spec.resolutions.all(x, has(self.spec.executionRequests) && self.spec.executionRequests.exists(r, r.stage == x.stage && r.requestID == x.requestID))",message="resolutions must bind an accepted request"
type HomeAssistantMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              HomeAssistantMigrationSpec   `json:"spec,omitempty"`
	Status            HomeAssistantMigrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type HomeAssistantMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HomeAssistantMigration `json:"items"`
}

func init() { SchemeBuilder.Register(&HomeAssistantMigration{}, &HomeAssistantMigrationList{}) }

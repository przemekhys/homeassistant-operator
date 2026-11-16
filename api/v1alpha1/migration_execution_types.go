package v1alpha1

// MigrationSecretIdentity pins credentials without storing their values.
type MigrationSecretIdentity struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ResourceVersion string `json:"resourceVersion"`
}

// MigrationObjectReference identifies one particular namespaced object.
type MigrationObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
}

// MigrationProxyCredentialBinding identifies immutable operational credentials
// without including private material. It is not proof of network isolation.
// +structType=atomic
type MigrationProxyCredentialBinding struct {
	// +kubebuilder:validation:Enum=v1
	Version string `json:"version"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	RequestID string                   `json:"requestID"`
	Bundle    MigrationObjectReference `json:"bundle"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	BundleResourceVersion string                   `json:"bundleResourceVersion"`
	Claim                 MigrationObjectReference `json:"claim"`
}

// MigrationEvidenceReference only permits a data key of a ConfigMap in this namespace.
type MigrationEvidenceReference struct {
	// +kubebuilder:validation:Enum=v1
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:Enum=ConfigMap
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	Key string `json:"key"`
}

// MigrationExecutionRequest is an accepted immutable snapshot, not proof of execution.
// +structType=atomic
// +kubebuilder:validation:XValidation:rule="self.stage == 'backup' ? (has(self.sourceEndpoint) && has(self.archiveStorage) && has(self.sourceToken) && has(self.recoveryKey) && !has(self.archiveRef) && !has(self.archiveDigest) && !has(self.archivePVCUID) && !has(self.sourceWorkload) && !has(self.uploadRequestID)) : (!has(self.sourceEndpoint) && !has(self.archiveStorage) && !has(self.sourceToken) && has(self.archiveRef) && has(self.archiveDigest) && has(self.archivePVCUID))",message="snapshot fields must match stage"
// +kubebuilder:validation:XValidation:rule="self.stage == 'restore' ? (has(self.uploadRequestID) && has(self.recoveryKey)) : (self.stage == 'backup' || (!has(self.uploadRequestID) && !has(self.recoveryKey)))",message="restore requires upload and recovery key"
type MigrationExecutionRequest struct {
	// +kubebuilder:validation:Enum=v1
	Version string `json:"version"`
	// +kubebuilder:validation:Enum=backup;upload;restore
	Stage string `json:"stage"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	OperationUID string `json:"operationUID"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	RequestID string `json:"requestID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	SourceID string `json:"sourceID"`
	// +kubebuilder:validation:MaxLength=2048
	SourceEndpoint string `json:"sourceEndpoint,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ReportID string          `json:"reportID"`
	Target   MigrationTarget `json:"target"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	ScopeDigest string `json:"scopeDigest"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	VerificationDigest string                    `json:"verificationDigest"`
	ArchiveStorage     *MigrationArchiveStorage  `json:"archiveStorage,omitempty"`
	SourceToken        *MigrationSecretIdentity  `json:"sourceToken,omitempty"`
	RecoveryKey        *MigrationSecretIdentity  `json:"recoveryKey,omitempty"`
	ArchiveRef         *MigrationObjectReference `json:"archiveRef,omitempty"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	ArchiveDigest string `json:"archiveDigest,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ArchivePVCUID  string                   `json:"archivePVCUID,omitempty"`
	SourceWorkload *MigrationSourceWorkload `json:"sourceWorkload,omitempty"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	UploadRequestID string `json:"uploadRequestID,omitempty"`
}

// MigrationExecutionAttempt prevents lost Jobs or receipts from permitting another POST.
// +structType=atomic
type MigrationExecutionAttempt struct {
	// +kubebuilder:validation:Enum=backup;upload;restore
	Stage string `json:"stage"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	RequestID string `json:"requestID"`
}

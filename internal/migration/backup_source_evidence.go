package migration

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const ArchiveValidationProfile = "ha-container-2026.11.0-securetar-v3"
const ArchiveValidatorVersion = "v1"

// BackupSourceEvidence is the final, bounded projection observed from the exact
// accepted result before any file is published. It contains no response bodies
// or credentials. The API transport must be independently qualified.
type BackupSourceEvidence struct {
	Version              string                     `json:"version"`
	OperationUID         string                     `json:"operationUID"`
	RequestID            string                     `json:"requestID"`
	SourceID             string                     `json:"sourceID"`
	TargetUID            string                     `json:"targetUID"`
	JobID                string                     `json:"jobID"`
	BackupID             string                     `json:"backupID"`
	AgentID              string                     `json:"agentID"`
	PVCUID               string                     `json:"pvcUID"`
	ScopeDigest          string                     `json:"scopeDigest"`
	VerificationDigest   string                     `json:"verificationDigest"`
	Size                 int64                      `json:"size"`
	Date                 string                     `json:"date"`
	HomeAssistantVersion string                     `json:"homeAssistantVersion"`
	IncludeDatabase      bool                       `json:"includeDatabase"`
	KeyRef               ha.MigrationSecretIdentity `json:"keyRef"`
}

type ArchiveValidation struct {
	Profile              string                     `json:"profile"`
	ValidatorVersion     string                     `json:"validatorVersion"`
	CiphertextDigest     string                     `json:"ciphertextDigest"`
	SourceEvidenceDigest string                     `json:"sourceEvidenceDigest"`
	KeyRef               ha.MigrationSecretIdentity `json:"keyRef"`
	PayloadAuthenticated bool                       `json:"payloadAuthenticated"`
	StructureValid       bool                       `json:"structureValid"`
}

// WorkerReceiptName is deterministic for a validated request, empty otherwise.
func WorkerReceiptName(requestID string) string {
	if !migrationprotocol.ValidDigest(requestID) {
		return ""
	}
	return "migration-receipt-" + strings.TrimPrefix(requestID, "sha256:")[:40]
}

func sourceEvidenceName(requestID string) string { return WorkerReceiptName(requestID) + "-source" }

func validateSourceEvidence(
	e BackupSourceEvidence, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
	pvc *corev1.PersistentVolumeClaim, receipt *BackupDispatchReceipt,
) error {
	if _, err := time.Parse(time.RFC3339Nano, e.Date); err != nil {
		return fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	if e.Version != "v1" || e.OperationUID != string(op.UID) || e.RequestID != r.RequestID ||
		e.SourceID != r.SourceID || e.TargetUID != r.Target.UID || e.JobID != receipt.JobID ||
		e.BackupID != receipt.JobID || !nativeBackupIDPattern.MatchString(e.BackupID) || e.AgentID == "" ||
		e.PVCUID != string(pvc.UID) || e.ScopeDigest != r.ScopeDigest || e.VerificationDigest != r.VerificationDigest ||
		e.Size <= 0 || e.Date == "" || e.HomeAssistantVersion != "2026.11.0" ||
		r.RecoveryKey == nil || e.KeyRef != *r.RecoveryKey {
		return fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	return nil
}

func finalBackupEvidence(
	details *haclient.BackupDetails, preflight *BackupPreflight, op *ha.HomeAssistantMigration,
	r ha.MigrationExecutionRequest, pvc *corev1.PersistentVolumeClaim, receipt *BackupDispatchReceipt,
) (BackupSourceEvidence, error) {
	var e BackupSourceEvidence
	if details == nil || preflight == nil || r.RecoveryKey == nil {
		return e, fmt.Errorf("BackupResultMismatch")
	}
	if details.Backup.ID != receipt.JobID || details.Backup.HomeAssistantVersion != "2026.11.0" ||
		details.Backup.Date == "" || !details.Backup.Automatic || details.AgentErrors == nil ||
		len(details.AgentErrors) != 0 || !backupResultScopeMatches(details.Backup, preflight) {
		return e, fmt.Errorf("BackupResultMismatch")
	}
	if digest, err := BackupScopeDigest(preflight); err != nil || digest != r.ScopeDigest {
		return e, fmt.Errorf("BackupResultMismatch")
	}
	for _, id := range preflight.AgentIDs {
		a, ok := details.Backup.Agents[id]
		if !ok || !a.Protected || a.Size <= 0 {
			return e, fmt.Errorf("BackupResultMismatch")
		}
	}
	agent := preflight.AgentIDs[0]
	e = BackupSourceEvidence{
		Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, SourceID: r.SourceID, TargetUID: r.Target.UID,
		JobID: receipt.JobID, BackupID: details.Backup.ID, AgentID: agent, PVCUID: string(pvc.UID),
		ScopeDigest:        r.ScopeDigest,
		VerificationDigest: r.VerificationDigest, Size: details.Backup.Agents[agent].Size, Date: details.Backup.Date,
		HomeAssistantVersion: details.Backup.HomeAssistantVersion, IncludeDatabase: *details.Backup.DatabaseIncluded,
		KeyRef: *r.RecoveryKey,
	}
	return e, validateSourceEvidence(e, op, r, pvc, receipt)
}

func backupResultScopeMatches(b haclient.BackupArchive, p *BackupPreflight) bool {
	return b.DatabaseIncluded != nil && b.HomeAssistantIncluded != nil && *b.HomeAssistantIncluded &&
		p.IncludeDatabase != nil && *b.DatabaseIncluded == *p.IncludeDatabase &&
		b.Folders != nil && len(b.Folders) == 0 && b.Addons != nil && len(b.Addons) == 0 &&
		b.FailedFolders != nil && len(b.FailedFolders) == 0 && b.FailedAddons != nil && len(b.FailedAddons) == 0 &&
		b.FailedAgentIDs != nil && len(b.FailedAgentIDs) == 0 && len(p.AgentIDs) > 0 && len(b.Agents) == len(p.AgentIDs)
}

func readSourceEvidence(
	ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
) (*BackupSourceEvidence, error) {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: op.Namespace, Name: sourceEvidenceName(r.RequestID)}
	if err := reader.Get(ctx, key, cm); err != nil {
		return nil, err
	}
	var e BackupSourceEvidence
	if !ownedImmutableEvidence(cm, op) || migrationprotocol.Decode([]byte(cm.Data["evidence.json"]), &e) != nil {
		return nil, fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	return &e, nil
}

// ReadBackupSourceEvidence returns only immutable evidence bound to this
// operation. Callers must still validate it against their specific receipt and
// storage bindings before treating it as archive provenance.
func ReadBackupSourceEvidence(
	ctx context.Context, reader client.Reader, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest,
) (*BackupSourceEvidence, error) {
	e, err := readSourceEvidence(ctx, reader, op, r)
	if err != nil {
		return nil, err
	}
	if e.Version != "v1" || e.OperationUID != string(op.UID) || e.RequestID != r.RequestID ||
		e.SourceID != r.SourceID || e.TargetUID != r.Target.UID {
		return nil, fmt.Errorf("ArchiveProvenanceUnavailable")
	}
	return e, nil
}

func ownedImmutableEvidence(cm *corev1.ConfigMap, op *ha.HomeAssistantMigration) bool {
	owner := metav1.GetControllerOf(cm)
	return owner != nil && owner.UID == op.UID && owner.Name == op.Name && owner.Kind == "HomeAssistantMigration" &&
		owner.APIVersion == ha.GroupVersion.String() && cm.Immutable != nil && *cm.Immutable &&
		len(cm.Data) == 1 && len(cm.BinaryData) == 0
}

// createImmutableEvidence also handles an ambiguous Create response by reading
// back exactly the same owned contents, never overwriting a collision.
func createImmutableEvidence(
	ctx context.Context, kube client.Client, scheme *runtime.Scheme, op *ha.HomeAssistantMigration,
	name, key string, value any,
) (*corev1.ConfigMap, error) {
	b, err := migrationprotocol.Canonical(value)
	if err != nil {
		return nil, fmt.Errorf("evidence encoding failed")
	}
	yes := true
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: op.Namespace}, Immutable: &yes,
		Data: map[string]string{key: string(b)},
	}
	if err := controllerutil.SetControllerReference(op, cm, scheme); err != nil {
		return nil, fmt.Errorf("evidence owner invalid")
	}
	if err := kube.Create(ctx, cm); err == nil {
		return cm, nil
	}
	existing := &corev1.ConfigMap{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(cm), existing); err != nil {
		return nil, fmt.Errorf("evidence publication unavailable")
	}
	if !ownedImmutableEvidence(existing, op) || !reflect.DeepEqual(existing.Data, cm.Data) {
		return nil, fmt.Errorf("evidence publication collision")
	}
	return existing, nil
}

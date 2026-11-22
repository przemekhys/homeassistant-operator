package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const migrationRequestBackupAnnotation = "ha.homeassistant.io/migration-request-backup"

type migrationSecretIdentity struct {
	Name            string `json:"name"`
	Key             string `json:"key"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
}

type migrationBackupRequest struct {
	Version        string                             `json:"version"`
	RequestID      string                             `json:"requestID"`
	OperationUID   string                             `json:"operationUID"`
	SourceID       string                             `json:"sourceID"`
	SourceEndpoint string                             `json:"sourceEndpoint"`
	Target         hav1alpha1.MigrationTarget         `json:"target"`
	ReportID       string                             `json:"reportID"`
	ScopeDigest    string                             `json:"scopeDigest"`
	ArchiveStorage hav1alpha1.MigrationArchiveStorage `json:"archiveStorage"`
	SourceToken    migrationSecretIdentity            `json:"sourceToken"`
	RecoveryKey    migrationSecretIdentity            `json:"recoveryKey"`
}

// acceptBackupRequest persists the approved snapshot using the resourceVersion
// read with the approval. It does not dispatch work or authorize a second POST.
func acceptBackupRequest(ctx context.Context, writer client.Client, reader client.Reader, observed *hav1alpha1.HomeAssistantMigration) (*hav1alpha1.MigrationExecutionRequest, error) {
	return acceptMigrationRequest(ctx, writer, reader, observed, "backup", migrationRequestObservation{})
}

func migrationSecretRefIdentity(ctx context.Context, reader client.Reader, namespace string, ref *corev1.SecretKeySelector) (migrationSecretIdentity, error) {
	if ref == nil || strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" || (ref.Optional != nil && *ref.Optional) {
		return migrationSecretIdentity{}, fmt.Errorf("required credential reference is missing")
	}
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, secret); err != nil || secret.UID == "" || secret.ResourceVersion == "" || len(secret.Data[ref.Key]) == 0 {
		return migrationSecretIdentity{}, fmt.Errorf("required credential is unavailable")
	}
	return migrationSecretIdentity{Name: ref.Name, Key: ref.Key, UID: string(secret.UID), ResourceVersion: secret.ResourceVersion}, nil
}

// Accepted inputs remain immutable even when later approvals change generation.
func validateAcceptedBackupRequest(op *hav1alpha1.HomeAssistantMigration) error {
	raw := op.Annotations[migrationRequestBackupAnnotation]
	if raw == "" {
		return nil
	}
	var request migrationBackupRequest
	if json.Unmarshal([]byte(raw), &request) != nil || request.Version != "v1" || request.RequestID == "" {
		return fmt.Errorf("invalid accepted backup request")
	}
	id := request.RequestID
	request.RequestID = ""
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("invalid accepted backup request")
	}
	digest := sha256.Sum256(encoded)
	if id != "sha256:"+hex.EncodeToString(digest[:]) || request.OperationUID != string(op.UID) || request.SourceID != op.Spec.SourceID || request.SourceEndpoint != op.Spec.SourceEndpoint || request.Target != op.Spec.Target || request.ReportID != op.Spec.ReportID || request.ScopeDigest != op.Spec.BackupScopeDigest || op.Spec.ArchiveStorage == nil || !reflect.DeepEqual(request.ArchiveStorage, *op.Spec.ArchiveStorage) || !migrationSecretRefMatches(op.Spec.SourceTokenSecretRef, request.SourceToken) || !migrationSecretRefMatches(op.Spec.RecoveryKeySecretRef, request.RecoveryKey) {
		return fmt.Errorf("accepted backup request inputs changed")
	}
	return nil
}

func migrationSecretRefMatches(ref *corev1.SecretKeySelector, identity migrationSecretIdentity) bool {
	return ref != nil && ref.Name == identity.Name && ref.Key == identity.Key && (ref.Optional == nil || !*ref.Optional) && identity.UID != "" && identity.ResourceVersion != ""
}

// Validation also runs in reconcile because webhook admission is optional.
//
//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func validateMigrationExecutionInputs(op *hav1alpha1.HomeAssistantMigration) error {
	if err := migrationprotocol.ValidateAccepted(op); err != nil {
		return err
	}
	if err := migrationprotocol.ValidateApprovals(op); err != nil {
		return err
	}
	if err := validateAcceptedBackupRequest(op); err != nil {
		return err
	}
	if endpoint := op.Spec.SourceEndpoint; endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || endpoint != op.Spec.SourceID || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid source endpoint")
		}
	}
	if len(op.Spec.Resolutions) > 64 {
		return fmt.Errorf("too many resolutions")
	}
	for _, resolution := range op.Spec.Resolutions {
		if resolution.Version != "v1" || strings.TrimSpace(resolution.RequestID) == "" || len(resolution.RequestID) > 128 ||
			(resolution.Stage != "backup" && resolution.Stage != "upload" && resolution.Stage != "restore") ||
			(resolution.Decision != "ObserveExternalResult" && resolution.Decision != "Abort") {
			return fmt.Errorf("invalid migration resolution")
		}
		var request struct {
			Version   string `json:"version"`
			RequestID string `json:"requestID"`
		}
		raw := op.Annotations["ha.homeassistant.io/migration-request-"+resolution.Stage]
		if op.Spec.ExecutionProtocol == "v1" {
			continue
		}
		if raw == "" || json.Unmarshal([]byte(raw), &request) != nil || request.Version != "v1" || request.RequestID != resolution.RequestID {
			return fmt.Errorf("migration resolution is not bound to an accepted request")
		}
	}
	if len(op.Spec.Verification) > 256 {
		return fmt.Errorf("too many verification items")
	}
	verificationIDs := make(map[string]bool, len(op.Spec.Verification))
	for _, item := range op.Spec.Verification {
		identity := item.Kind + "\x00" + item.ID
		if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.ExpectedDigest) == "" || verificationIDs[identity] {
			return fmt.Errorf("invalid verification item")
		}
		verificationIDs[identity] = true
	}
	return nil
}

func migrationVerificationDigest(items []hav1alpha1.MigrationVerificationItem) (string, error) {
	return migrationprotocol.VerificationDigest(items)
}

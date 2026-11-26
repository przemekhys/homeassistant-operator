package migrationprotocol

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func RequestID(r ha.MigrationExecutionRequest) (string, error) {
	b, err := Canonical(r)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if Decode(b, &m) != nil {
		return "", fmt.Errorf("invalid request")
	}
	delete(m, "requestID")
	return Digest(m)
}

func ParseRequest(raw []byte) (*ha.MigrationExecutionRequest, error) {
	var r ha.MigrationExecutionRequest
	if Decode(raw, &r) != nil {
		return nil, fmt.Errorf("invalid request")
	}
	if err := ValidateRequest(r); err != nil {
		return nil, err
	}
	return &r, nil
}

func validSecretIdentity(s *ha.MigrationSecretIdentity) bool {
	return s != nil && len(validation.IsDNS1123Subdomain(s.Name)) == 0 && len(validation.IsConfigMapKey(s.Key)) == 0 && s.UID != "" && s.ResourceVersion != ""
}

func ValidateCredentialVersion(expected *ha.MigrationSecretIdentity, observed *corev1.Secret) error {
	if !validSecretIdentity(expected) || observed == nil || expected.Name != observed.Name || expected.UID != string(observed.UID) || expected.ResourceVersion != observed.ResourceVersion || len(observed.Data[expected.Key]) == 0 {
		return fmt.Errorf("CredentialVersionChanged")
	}
	return nil
}

//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func ValidateRequest(r ha.MigrationExecutionRequest) error {
	bad := fmt.Errorf("invalid execution request")
	if r.Version != "v1" || !ValidStage(r.Stage) || r.OperationUID == "" || r.SourceID == "" || r.Target.Name == "" || r.Target.UID == "" || r.ReportID == "" || !ValidDigest(r.ScopeDigest) || !ValidDigest(r.VerificationDigest) || !ValidDigest(r.RequestID) {
		return bad
	}
	if r.Stage == "backup" {
		u, err := url.Parse(r.SourceEndpoint)
		if err != nil || r.SourceEndpoint != r.SourceID || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !validSecretIdentity(r.SourceToken) || !validSecretIdentity(r.RecoveryKey) || r.ArchiveStorage == nil || len(validation.IsDNS1123Subdomain(r.ArchiveStorage.PVCName)) != 0 || len(validation.IsDNS1123Subdomain(r.ArchiveStorage.DescriptorName)) != 0 || r.ArchiveRef != nil || r.ArchiveDigest != "" || r.ArchivePVCUID != "" || r.SourceWorkload != nil || r.UploadRequestID != "" {
			return bad
		}
	} else {
		if r.SourceEndpoint != "" || r.SourceToken != nil || r.ArchiveStorage != nil || r.ArchiveRef == nil || len(validation.IsDNS1123Subdomain(r.ArchiveRef.Name)) != 0 || r.ArchiveRef.UID == "" || !ValidDigest(r.ArchiveDigest) || r.ArchivePVCUID == "" {
			return bad
		}
		if r.Stage == "restore" {
			if !validSecretIdentity(r.RecoveryKey) || !ValidDigest(r.UploadRequestID) {
				return bad
			}
		} else if r.UploadRequestID != "" || r.RecoveryKey != nil {
			return bad
		}
		if w := r.SourceWorkload; w != nil && ((w.Kind != "Deployment" && w.Kind != "StatefulSet") || len(validation.IsDNS1123Subdomain(w.Name)) != 0 || w.UID == "") {
			return bad
		}
	}
	id, err := RequestID(r)
	if err != nil || id != r.RequestID {
		return bad
	}
	return nil
}

// ValidateAccepted checks durable snapshots independently of mutable future inputs
// and current approvals. Revocation does not retroactively erase acceptance.
//
//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func ValidateAccepted(op *ha.HomeAssistantMigration) error {
	if op.Spec.ExecutionProtocol != "" && op.Spec.ExecutionProtocol != "v1" {
		return fmt.Errorf("unsupported execution protocol")
	}
	if len(op.Spec.ExecutionRequests) > 3 || len(op.Spec.ExecutionAttempts) > 3 || len(op.Spec.ExecutionDispatches) > 3 {
		return fmt.Errorf("too many execution records")
	}
	if len(op.Spec.Verification) > 0 {
		if _, err := VerificationDigest(op.Spec.Verification); err != nil {
			return err
		}
	}
	seen := map[string]ha.MigrationExecutionRequest{}
	for _, r := range op.Spec.ExecutionRequests {
		vd, err := VerificationDigest(op.Spec.Verification)
		if op.Spec.ExecutionProtocol != "v1" || ValidateRequest(r) != nil || err != nil || r.VerificationDigest != vd || r.OperationUID != string(op.UID) || r.SourceID != op.Spec.SourceID || r.Target != op.Spec.Target || r.ReportID != op.Spec.ReportID {
			return fmt.Errorf("execution request binding is invalid")
		}
		if _, exists := seen[r.Stage]; exists {
			return fmt.Errorf("duplicate execution stage")
		}
		seen[r.Stage] = r
	}
	if r, ok := seen["restore"]; ok {
		u, ok := seen["upload"]
		if !ok || r.UploadRequestID != u.RequestID || r.ArchiveDigest != u.ArchiveDigest || !reflect.DeepEqual(r.ArchiveRef, u.ArchiveRef) || r.ArchivePVCUID != u.ArchivePVCUID || r.ScopeDigest != u.ScopeDigest || !reflect.DeepEqual(r.SourceWorkload, u.SourceWorkload) {
			return fmt.Errorf("restore does not bind accepted upload")
		}
	}
	if binding := op.Spec.ProxyCredentialBinding; binding != nil {
		if binding.Version != "v1" || !ValidDigest(binding.RequestID) || seen["upload"].RequestID != binding.RequestID ||
			len(validation.IsDNS1123Subdomain(binding.Bundle.Name)) != 0 || binding.Bundle.UID == "" ||
			binding.BundleResourceVersion == "" || len(validation.IsDNS1123Subdomain(binding.Claim.Name)) != 0 || binding.Claim.UID == "" {
			return fmt.Errorf("proxy credential binding invalid")
		}
	}
	attempts := map[string]bool{}
	for _, a := range op.Spec.ExecutionAttempts {
		if attempts[a.Stage] || seen[a.Stage].RequestID != a.RequestID || !ValidDigest(a.RequestID) {
			return fmt.Errorf("invalid execution attempt")
		}
		attempts[a.Stage] = true
	}
	dispatches := map[string]bool{}
	for _, d := range op.Spec.ExecutionDispatches {
		if dispatches[d.Stage] || !attempts[d.Stage] || seen[d.Stage].RequestID != d.RequestID || !ValidDigest(d.RequestID) {
			return fmt.Errorf("invalid execution dispatch")
		}
		dispatches[d.Stage] = true
	}
	observations := map[string]bool{}
	for _, resolution := range op.Spec.Resolutions {
		if resolution.Version != "v1" || !ValidStage(resolution.Stage) || !ValidDigest(resolution.RequestID) || (op.Spec.ExecutionProtocol == "v1" && seen[resolution.Stage].RequestID != resolution.RequestID) {
			return fmt.Errorf("invalid migration resolution binding")
		}
		switch resolution.Decision {
		case "ObserveExternalResult":
			key := resolution.Stage + "\x00" + resolution.RequestID
			if observations[key] {
				return fmt.Errorf("duplicate observation resolution")
			}
			observations[key] = true
			if err := ValidateEvidenceRef(resolution.EvidenceRef); err != nil {
				return err
			}
		case "Abort":
			if resolution.EvidenceRef != nil {
				return fmt.Errorf("abort cannot carry evidence")
			}
		default:
			return fmt.Errorf("invalid migration resolution")
		}
	}
	return nil
}

// ValidateNewRequest enforces consent only at acceptance, not on historical records.
//
//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func ValidateNewRequest(op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) error {
	if op.Spec.ExecutionProtocol != "v1" || op.Spec.CancelRequested || !op.DeletionTimestamp.IsZero() || AbortRequested(op) {
		return fmt.Errorf("execution acceptance is blocked")
	}
	if err := ValidateRequest(r); err != nil {
		return err
	}
	vd, err := VerificationDigest(op.Spec.Verification)
	if err != nil || vd != r.VerificationDigest || r.OperationUID != string(op.UID) || r.SourceID != op.Spec.SourceID || r.Target != op.Spec.Target || r.ReportID != op.Spec.ReportID {
		return fmt.Errorf("request does not bind operation")
	}
	a := map[string]ha.MigrationApproval{}
	for _, x := range op.Spec.Approvals {
		if _, ok := a[x.ID]; ok {
			return fmt.Errorf("duplicate approval")
		}
		a[x.ID] = x
	}
	bound := func(id string, verification bool) bool {
		x := a[id]
		return x.Approved && x.TargetUID == r.Target.UID && (!verification || x.VerificationDigest == vd)
	}
	if r.Stage == "backup" {
		for _, id := range []string{"backup", "backup-settings-stable"} {
			x := a[id]
			if !bound(id, id == "backup") || x.Digest != r.ReportID || x.ScopeDigest != r.ScopeDigest {
				return fmt.Errorf("backup approval is not bound")
			}
		}
		if r.SourceEndpoint != op.Spec.SourceEndpoint || r.ScopeDigest != op.Spec.BackupScopeDigest || !reflect.DeepEqual(r.ArchiveStorage, op.Spec.ArchiveStorage) || !secretRefMatches(op.Spec.SourceTokenSecretRef, r.SourceToken) || !secretRefMatches(op.Spec.RecoveryKeySecretRef, r.RecoveryKey) {
			return fmt.Errorf("backup inputs changed")
		}
	} else {
		x := a["restore"]
		if !bound("restore", true) || strings.TrimSpace(x.Digest) == "" || x.ArchiveDigest != r.ArchiveDigest || x.ScopeDigest != r.ScopeDigest || !bound("source-stop", false) || a["source-stop"].Digest != op.Spec.ReportID || op.Spec.Archive == nil || op.Spec.Archive.BackupID == "" || op.Spec.Archive.AgentID == "" || op.Spec.Archive.Digest != r.ArchiveDigest || op.Spec.RestoreScopeDigest != r.ScopeDigest || !reflect.DeepEqual(r.SourceWorkload, op.Spec.SourceWorkload) {
			return fmt.Errorf("restore approval is not bound")
		}
		if w := r.SourceWorkload; w != nil && a["source-stop"].SourceUID != w.UID {
			return fmt.Errorf("source stop approval is not bound")
		}
		if r.Stage == "restore" {
			found := false
			for _, u := range op.Spec.ExecutionRequests {
				if u.Stage == "upload" && u.RequestID == r.UploadRequestID && u.ArchiveDigest == r.ArchiveDigest && reflect.DeepEqual(u.ArchiveRef, r.ArchiveRef) && u.ArchivePVCUID == r.ArchivePVCUID && u.ScopeDigest == r.ScopeDigest && reflect.DeepEqual(u.SourceWorkload, r.SourceWorkload) {
					found = true
				}
			}
			if !found || !secretRefMatches(op.Spec.RecoveryKeySecretRef, r.RecoveryKey) {
				return fmt.Errorf("restore inputs changed")
			}
		}
	}
	return nil
}

// AbortRequested blocks future local work. It does not assert that a prior
// external action has stopped or been rolled back.
func AbortRequested(op *ha.HomeAssistantMigration) bool {
	if op == nil {
		return false
	}
	for _, resolution := range op.Spec.Resolutions {
		if resolution.Version != "v1" || resolution.Decision != "Abort" || resolution.EvidenceRef != nil {
			continue
		}
		for _, request := range op.Spec.ExecutionRequests {
			if resolution.Stage == request.Stage && resolution.RequestID == request.RequestID {
				return true
			}
		}
	}
	return false
}

func secretRefMatches(ref *corev1.SecretKeySelector, id *ha.MigrationSecretIdentity) bool {
	return ref != nil && id != nil && ref.Name == id.Name && ref.Key == id.Key && (ref.Optional == nil || !*ref.Optional)
}

func ValidateApprovals(op *ha.HomeAssistantMigration) error {
	if op.Spec.ExecutionProtocol != "v1" {
		return nil
	}
	seen := make(map[string]bool, len(op.Spec.Approvals))
	for _, a := range op.Spec.Approvals {
		if strings.TrimSpace(a.ID) == "" || seen[a.ID] {
			return fmt.Errorf("invalid or duplicate approval")
		}
		seen[a.ID] = true
		if !a.Approved {
			continue
		}
		switch a.ID {
		case "backup", "restore", "resume", "manual-cutover":
			vd, err := VerificationDigest(op.Spec.Verification)
			if err != nil || vd != a.VerificationDigest {
				return fmt.Errorf("approval verification digest is not bound")
			}
			if a.ID == "resume" && !ValidDigest(a.Digest) {
				return fmt.Errorf("resume must bind verified result")
			}
		}
	}
	return nil
}

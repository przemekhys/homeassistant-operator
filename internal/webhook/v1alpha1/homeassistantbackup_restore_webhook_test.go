package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func validRestore() *hav1alpha1.HomeAssistantRestore {
	return &hav1alpha1.HomeAssistantRestore{Spec: hav1alpha1.HomeAssistantRestoreSpec{
		HomeAssistantRef:           hav1alpha1.HomeAssistantTargetReference{Name: "home", UID: "target-uid"},
		BackupID:                   "backup-id",
		Source:                     hav1alpha1.RestoreSource{AgentID: "backup.local"},
		EncryptionKeySecretRef:     hav1alpha1.SecretKeyReference{Name: "key", Key: "password"},
		PostRestoreAccessSecretRef: hav1alpha1.SecretKeyReference{Name: "token", Key: "token"},
		Scope:                      hav1alpha1.RestoreScope{HomeAssistant: true},
		SafetyBackupPolicy:         "Skip",
	}}
}

func TestRestoreWebhookRejectsUnsafeInput(t *testing.T) {
	v := &restoreValidator{}
	ctx := context.Background()
	if _, err := v.ValidateCreate(ctx, validRestore()); err != nil {
		t.Fatalf("valid restore rejected: %v", err)
	}
	bad := validRestore()
	bad.Spec.Source.Uploaded = &hav1alpha1.UploadedRestoreSource{}
	if _, err := v.ValidateCreate(ctx, bad); err == nil {
		t.Fatal("ambiguous source accepted")
	}
	bad = validRestore()
	bad.Spec.Scope.HomeAssistant = false
	if _, err := v.ValidateCreate(ctx, bad); err == nil {
		t.Fatal("partial scope accepted")
	}
}

func TestRestoreWebhookRejectsSpecMutationAndMalformedApproval(t *testing.T) {
	v := &restoreValidator{}
	old := validRestore()
	old.ObjectMeta = metav1.ObjectMeta{Name: "restore"}
	changed := old.DeepCopy()
	changed.Spec.BackupID = "other"
	if _, err := v.ValidateUpdate(context.Background(), old, changed); err == nil {
		t.Fatal("spec mutation accepted")
	}
	approval := old.DeepCopy()
	approval.Annotations = map[string]string{restoreApprovalAnnotation: "{"}
	if _, err := v.ValidateUpdate(context.Background(), old, approval); err == nil {
		t.Fatal("malformed approval accepted")
	}
}

func TestBackupWebhookRejectsMissingTargetAndSpecMutation(t *testing.T) {
	v := &backupValidator{}
	ctx := context.Background()
	backup := &hav1alpha1.HomeAssistantBackup{Spec: hav1alpha1.HomeAssistantBackupSpec{
		HomeAssistantRef: hav1alpha1.HomeAssistantTargetReference{Name: "home", UID: "target-uid"},
	}}
	if _, err := v.ValidateCreate(ctx, backup); err != nil {
		t.Fatalf("valid backup rejected: %v", err)
	}
	missingTarget := backup.DeepCopy()
	missingTarget.Spec.HomeAssistantRef.UID = ""
	if _, err := v.ValidateCreate(ctx, missingTarget); err == nil {
		t.Fatal("backup without target UID accepted")
	}
	changed := backup.DeepCopy()
	changed.Spec.Name = "different"
	if _, err := v.ValidateUpdate(ctx, backup, changed); err == nil {
		t.Fatal("backup spec mutation accepted")
	}
}

func TestRestoreWebhookApprovalRequiresCompleteJSONBinding(t *testing.T) {
	v := &restoreValidator{}
	old := validRestore()
	old.UID = types.UID("restore-uid")

	for name, approval := range map[string]string{
		"not-json":       "{",
		"missing-target": `{"operationUID":"restore-uid","planDigest":"digest","challenge":"challenge"}`,
		"missing-plan":   `{"operationUID":"restore-uid","targetUID":"target-uid","challenge":"challenge"}`,
		"missing-proof":  `{"operationUID":"restore-uid","targetUID":"target-uid","planDigest":"digest"}`,
	} {
		t.Run(name, func(t *testing.T) {
			updated := old.DeepCopy()
			updated.Annotations = map[string]string{restoreApprovalAnnotation: approval}
			if _, err := v.ValidateUpdate(context.Background(), old, updated); err == nil {
				t.Fatal("incomplete approval accepted")
			}
		})
	}

	updated := old.DeepCopy()
	updated.Annotations = map[string]string{
		restoreApprovalAnnotation: `{"operationUID":"restore-uid","targetUID":"target-uid",` +
			`"planDigest":"digest","challenge":"challenge"}`,
	}
	if _, err := v.ValidateUpdate(context.Background(), old, updated); err != nil {
		t.Fatalf("complete approval rejected: %v", err)
	}
}

func TestRestoreValidationIsSafeWithoutCELOrWebhookDelivery(t *testing.T) {
	// This is the controller-independent validation path used on Kubernetes
	// versions without CEL support. It must reject unsafe input rather than
	// relying on the API server to have delivered a webhook request.
	unsafe := validRestore()
	unsafe.Spec.Source = hav1alpha1.RestoreSource{}
	if err := validateRestore(unsafe); err == nil {
		t.Fatal("restore with no source accepted by fallback validation")
	}
	unsafe = validRestore()
	unsafe.Spec.HomeAssistantRef.UID = ""
	if err := validateRestore(unsafe); err == nil {
		t.Fatal("restore without target UID accepted by fallback validation")
	}
}

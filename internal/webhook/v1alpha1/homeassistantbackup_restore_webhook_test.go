package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

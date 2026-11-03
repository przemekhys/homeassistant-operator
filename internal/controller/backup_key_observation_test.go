package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
)

func TestBackupKeyObservationDelayIsJitteredAndStable(t *testing.T) {
	first := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "first", UID: types.UID("first")},
	}
	second := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "second", UID: types.UID("second")},
	}
	got := backupKeyObservationDelay(first)
	if got < 54*time.Second || got > 66*time.Second {
		t.Fatalf("delay = %s, want 54s through 66s", got)
	}
	if again := backupKeyObservationDelay(first); again != got {
		t.Fatalf("delay changed for same instance: %s then %s", got, again)
	}
	if backupKeyObservationDelay(second) == got {
		t.Fatal("distinct instances unexpectedly received the same test jitter")
	}
}

func TestBackupKeyObservationDelayFallsBackBeforeUIDAssignment(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "pending"}}
	if got := backupKeyObservationDelay(ha); got < 54*time.Second || got > 66*time.Second {
		t.Fatalf("delay = %s, want 54s through 66s", got)
	}
}

func TestObserveBackupKeyUsesKeyProtectionPermitDuringRestoreHold(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "ha", UID: types.UID("ha-uid")}}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := coordination.Store{Reader: c, Client: c}
	if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHold(ctx, ha.Namespace, ha.UID, types.UID("restore"), "restore"); err != nil {
		t.Fatal(err)
	}

	password := "test-key-canary"
	(&HomeAssistantReconciler{Client: c, APIReader: c}).observeBackupKey(ctx, ha, &password)

	secrets := &corev1.SecretList{}
	if err := c.List(ctx, secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("retained Secret count = %d, want 1", len(secrets.Items))
	}
	if len(secrets.Items[0].Data[backupKeySecretDataKey]) == 0 {
		t.Fatal("observation wrote an empty backup key")
	}
	root, err := store.GetRoot(ctx, ha.Namespace, ha.UID)
	if err != nil {
		t.Fatal(err)
	}
	if root.ActivePermit != nil {
		t.Fatalf("KeyProtection permit was not released: %#v", root.ActivePermit)
	}
}

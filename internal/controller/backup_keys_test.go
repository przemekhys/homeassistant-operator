package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

func TestProtectBackupKeyRetainsDistinctValuesAndReusesExistingEntry(t *testing.T) {
	r, ha := backupKeyTestReconciler(t, "home", "instance-uid")
	first, second, firstAgain := "key-one", "key-two", "key-one"
	firstRef, err := r.protectBackupKey(context.Background(), ha, &first)
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := r.protectBackupKey(context.Background(), ha, &second)
	if err != nil {
		t.Fatal(err)
	}
	againRef, err := r.protectBackupKey(context.Background(), ha, &firstAgain)
	if err != nil {
		t.Fatal(err)
	}
	if firstRef.Key != "key-000001" || secondRef.Key != "key-000002" || againRef.Key != firstRef.Key {
		t.Fatalf("unexpected references: first=%#v second=%#v again=%#v", firstRef, secondRef, againRef)
	}
	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ha.Namespace, Name: firstRef.Name}, secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data) != 2 || len(secret.OwnerReferences) != 0 || len(secret.Finalizers) != 0 {
		t.Fatalf("retained secret mutated incorrectly: %#v", secret)
	}
}

func TestProtectBackupKeyRejectsForeignSecret(t *testing.T) {
	r, ha := backupKeyTestReconciler(t, "home", "instance-uid")
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: backupKeySecretName(ha), Namespace: ha.Namespace,
		Labels: map[string]string{backupKeyUIDLabel: "other-instance"},
	}}
	if err := r.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	value := "key-one"
	if _, err := r.protectBackupKey(context.Background(), ha, &value); err == nil {
		t.Fatal("foreign retained Secret was accepted")
	}
}

func TestProtectBackupKeyIgnoresMissingValue(t *testing.T) {
	r, ha := backupKeyTestReconciler(t, "home", "instance-uid")
	ref, err := r.protectBackupKey(context.Background(), ha, nil)
	if err != nil || ref != nil {
		t.Fatalf("missing key result = %#v, %v", ref, err)
	}
	secret := &corev1.Secret{}
	err = r.Get(context.Background(), types.NamespacedName{Namespace: ha.Namespace, Name: backupKeySecretName(ha)}, secret)
	if err == nil {
		t.Fatal("missing key created a retained Secret")
	}
}

func TestBackupKeySecretNameIsBoundedAndUIDSpecific(t *testing.T) {
	longName := strings.Repeat("a", 63)
	_, first := backupKeyTestReconciler(t, longName, "first-uid-123456")
	_, second := backupKeyTestReconciler(t, longName, "second-uid-12345")
	if got := backupKeySecretName(first); len(got) > 63 {
		t.Fatalf("name length = %d, want <= 63: %q", len(got), got)
	}
	if backupKeySecretName(first) == backupKeySecretName(second) {
		t.Fatal("different instance UIDs produced the same retained Secret name")
	}
}

func TestProtectBackupKeyRejectsSecretSizeLimitWithoutDroppingExistingValues(t *testing.T) {
	r, ha := backupKeyTestReconciler(t, "home", "instance-uid")
	first := "key-one"
	ref, err := r.protectBackupKey(context.Background(), ha, &first)
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", maxBackupKeySecretBytes)
	if _, err := r.protectBackupKey(context.Background(), ha, &oversized); err == nil {
		t.Fatal("oversized key was accepted")
	}
	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ha.Namespace, Name: ref.Name}, secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data) != 1 || string(secret.Data[ref.Key]) != first {
		t.Fatal("size rejection modified retained key values")
	}
}

func backupKeyTestReconciler(t *testing.T, name, uid string) (*HomeAssistantReconciler, *hav1.HomeAssistant) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(uid)}}
	return &HomeAssistantReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}, ha
}

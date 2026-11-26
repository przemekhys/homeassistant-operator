package migration

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func TestCreateImportedSecretIsCreateOnlyOwnedAndDoesNotLeakValues(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	migration := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration",
			Namespace: "default",
			UID:       "migration-uid",
		},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	const value = "available-but-private"
	secret, err := CreateImportedSecret(
		context.Background(),
		kube,
		scheme,
		migration,
		map[string][]byte{"integration.token": []byte(value)},
	)
	if err != nil || len(secret.OwnerReferences) != 1 ||
		secret.OwnerReferences[0].UID != migration.UID {
		t.Fatalf("CreateImportedSecret() = %#v, %v", secret, err)
	}
	if secret.Immutable == nil || !*secret.Immutable {
		t.Fatal("imported secret is not immutable")
	}
	if _, err := CreateImportedSecret(context.Background(), kube, scheme, migration, map[string][]byte{"integration.token": []byte(value)}); err != nil {
		t.Fatalf("identical retry failed: %v", err)
	}
	if _, err := CreateImportedSecret(context.Background(), kube, scheme, migration, map[string][]byte{"integration.token": []byte("different")}); err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("unsafe collision result: %v", err)
	}
	other := migration.DeepCopy()
	other.UID = "another-uid"
	if _, err := CreateImportedSecret(context.Background(), kube, scheme, other, map[string][]byte{"integration.token": []byte(value)}); err == nil {
		t.Fatal("accepted a Secret owned by a different operation")
	}
	stored := &corev1.Secret{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "migration-import"}, stored); err != nil {
		t.Fatal(err)
	}
	if string(stored.Data["integration.token"]) != value {
		t.Fatal("collision changed an existing Secret")
	}
}

func TestCreateImportedSecretRejectsUnavailableValues(t *testing.T) {
	if _, err := CreateImportedSecret(context.Background(), nil, nil, nil, nil); err == nil {
		t.Fatal("missing Secret inputs were accepted")
	}
}

func TestImportedSecretLongName(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	m := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 253), Namespace: "default", UID: "uid"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	secret, err := CreateImportedSecret(context.Background(), kube, scheme, m, map[string][]byte{"key": []byte("value")})
	if err != nil || len(secret.Name) > 253 {
		t.Fatalf("invalid generated name: %v", err)
	}
}

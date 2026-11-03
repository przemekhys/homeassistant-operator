package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

func TestObserveBackupKeyPublishesSafeRetentionReference(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Namespace: "test", Name: "home", UID: types.UID("home-uid"),
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	password := "backup-key-canary-must-not-escape"

	(&HomeAssistantReconciler{Client: client, APIReader: client}).observeBackupKey(ctx, ha, &password)

	condition := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupKeySecured)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != reasonBackupKeySecured {
		t.Fatalf("BackupKeySecured condition = %#v", condition)
	}
	secrets := &corev1.SecretList{}
	if err := client.List(ctx, secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("retained Secret count = %d, want 1", len(secrets.Items))
	}
	if !strings.Contains(condition.Message, secrets.Items[0].Name) {
		t.Fatalf("condition does not reference retained Secret: %q", condition.Message)
	}
	statusJSON, err := json.Marshal(ha.Status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(statusJSON), password) {
		t.Fatalf("backup key leaked into status: %s", statusJSON)
	}
}

func TestObserveBackupKeyReportsRetentionFailureIndependently(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "home"}}
	password := "backup-key-canary-must-not-escape"

	(&HomeAssistantReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}).observeBackupKey(
		context.Background(),
		ha,
		&password,
	)

	condition := meta.FindStatusCondition(ha.Status.Conditions, conditionTypeBackupKeySecured)
	if condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != reasonBackupKeyRetentionFailed {
		t.Fatalf("BackupKeySecured condition = %#v", condition)
	}
	if strings.Contains(condition.Message, password) {
		t.Fatalf("backup key leaked into failure condition: %q", condition.Message)
	}
}

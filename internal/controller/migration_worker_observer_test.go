package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMigrationWorkerObserverBindsAndReportsBlocker(t *testing.T) {
	ctx := context.Background()
	old, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, old, old, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	op.Spec.ExecutionDispatches = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
	s := runtime.NewScheme()
	if err := ha.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	owner := metav1.OwnerReference{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: op.Name, UID: op.UID, Controller: workerBool(true)}
	receipt := migration.BackupDispatchReceipt{RequestID: r.RequestID, OperationUID: string(op.UID), TargetUID: r.Target.UID, ReportID: r.ReportID, ScopeDigest: r.ScopeDigest, Phase: "Accepted", JobID: "job"}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "migration-receipt-" + strings.TrimPrefix(r.RequestID, "sha256:")[:40], Namespace: op.Namespace, OwnerReferences: []metav1.OwnerReference{owner}}, Data: map[string]string{"receipt.json": string(data)}}
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(op, cm).Build()
	observer := &migrationWorkerObserver{reader: kube}
	observed, err := observer.Observe(ctx, op, *r)
	if err != nil {
		t.Fatal(err)
	}
	if phase, _ := migrationObservedStage(*r, observed); phase != "BackupInProgress" {
		t.Fatalf("phase=%s", phase)
	}
	receipt.ArchiveDigest = "sha256:" + strings.Repeat("a", 64)
	receipt.ArchiveSize = 10
	receipt.ArchivePVCUID = "pvc"
	receipt.BlockedReason = "StoredArchiveAuthenticationUnavailable"
	data, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["receipt.json"] = string(data)
	if err := kube.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	observed, err = observer.Observe(ctx, op, *r)
	if err != nil {
		t.Fatal(err)
	}
	if phase, reason := migrationObservedStage(*r, observed); phase != "Blocked" || reason != "StoredArchiveAuthenticationUnavailable" {
		t.Fatalf("phase=%s reason=%s", phase, reason)
	}
	foreign := op.DeepCopy()
	foreign.Spec.ExecutionDispatches = nil
	if _, err := observer.Observe(ctx, foreign, *r); err == nil {
		t.Fatal("receipt without dispatch marker accepted")
	}
}

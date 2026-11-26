package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type dispatchSource struct {
	backupReader
	calls       int
	id          string
	errDispatch error
	onDispatch  func()
}

type failingDispatchStore struct {
	BackupDispatchStore
	claimFailure bool
}

type concurrentDispatchSource struct {
	backupReader
	calls atomic.Int32
}

func (s *concurrentDispatchSource) GenerateBackupWithAutomaticSettings(context.Context, string) (string, error) {
	s.calls.Add(1)
	return "job-id", nil
}

func (s failingDispatchStore) Claim(ctx context.Context, receipt BackupDispatchReceipt) (bool, error) {
	if s.claimFailure {
		return false, errors.New("sensitive-key")
	}
	return s.BackupDispatchStore.Claim(ctx, receipt)
}

func (s failingDispatchStore) SaveAccepted(context.Context, BackupDispatchReceipt) error {
	return errors.New("sensitive-key")
}

func (s *dispatchSource) GenerateBackupWithAutomaticSettings(context.Context, string) (string, error) {
	s.calls++
	if s.onDispatch != nil {
		s.onDispatch()
	}
	return s.id, s.errDispatch
}

func TestDispatchBackupOncePersistsBeforeSendingAndNeverBlindlyRetries(t *testing.T) {
	include := true
	password := "sensitive-key"
	for _, tc := range []struct {
		name, id    string
		dispatchErr error
		phase       string
	}{
		{"accepted", "backup-id", nil, "Accepted"},
		{"lost response", "", errors.New("sensitive-key"), "Dispatching"},
		{"malformed response", "private token", nil, "Dispatching"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := hav1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			operation := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "operation-uid"}}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(operation).Build()
			store := &ConfigMapBackupDispatchStore{Client: kube, Scheme: scheme, Operation: operation}
			source := &dispatchSource{backupReader: backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
				IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password,
			}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}, id: tc.id, errDispatch: tc.dispatchErr}
			digest, err := BackupScopeDigest(observeBackupPreflight(context.Background(), source, "private-token"))
			if err != nil {
				t.Fatal(err)
			}
			intent := BackupDispatchReceipt{OperationUID: "operation-uid", TargetUID: "target", ReportID: "report", ScopeDigest: digest}
			operation.Spec = hav1alpha1.HomeAssistantMigrationSpec{
				Target: hav1alpha1.MigrationTarget{UID: intent.TargetUID}, ReportID: intent.ReportID, BackupScopeDigest: digest,
				Approvals: []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true, TargetUID: intent.TargetUID, Digest: intent.ReportID, ScopeDigest: digest}},
			}
			if err := kube.Update(context.Background(), operation); err != nil {
				t.Fatal(err)
			}
			source.onDispatch = func() {
				cm := &corev1.ConfigMap{}
				if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: BackupDispatchReceiptName("operation-uid")}, cm); err != nil {
					t.Fatal("backup dispatched before durable claim")
				}
				var claimed BackupDispatchReceipt
				if json.Unmarshal([]byte(cm.Data[backupDispatchReceiptKey]), &claimed) != nil || claimed.Phase != "Dispatching" {
					t.Fatal("invalid claim before dispatch")
				}
			}
			_, err = DispatchBackupOnce(context.Background(), store, source, "private-token", []byte(password), intent)
			if tc.phase == "Accepted" && err != nil || tc.phase != "Accepted" && err == nil {
				t.Fatalf("unexpected result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), password) {
				t.Fatal("error exposed recovery key")
			}
			cm := &corev1.ConfigMap{}
			if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: BackupDispatchReceiptName("operation-uid")}, cm); err != nil {
				t.Fatal(err)
			}
			owner := metav1.GetControllerOf(cm)
			if owner == nil || owner.UID != operation.UID {
				t.Fatal("receipt is not owned by operation")
			}
			var persisted BackupDispatchReceipt
			if err := json.Unmarshal([]byte(cm.Data[backupDispatchReceiptKey]), &persisted); err != nil || persisted.Phase != tc.phase {
				t.Fatalf("receipt=%#v err=%v", persisted, err)
			}
			if strings.Contains(cm.Data[backupDispatchReceiptKey], password) || strings.Contains(cm.Data[backupDispatchReceiptKey], "private-token") {
				t.Fatal("receipt contains credentials")
			}
			if _, err := DispatchBackupOnce(context.Background(), store, source, "private-token", []byte(password), intent); err == nil || source.calls != 1 {
				t.Fatalf("unsafe retry: calls=%d err=%v", source.calls, err)
			}
		})
	}
}

func TestDispatchBackupOnceRejectsChangedScopeBeforeClaim(t *testing.T) {
	include := true
	password := "key"
	source := &dispatchSource{backupReader: backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
		IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password,
	}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = hav1alpha1.AddToScheme(scheme)
	operation := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "uid"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &ConfigMapBackupDispatchStore{Client: kube, Scheme: scheme, Operation: operation}
	intent := BackupDispatchReceipt{OperationUID: "uid", TargetUID: "target", ReportID: "report", ScopeDigest: "wrong"}
	if _, err := DispatchBackupOnce(context.Background(), store, source, "token", []byte(password), intent); err == nil || source.calls != 0 {
		t.Fatal("changed scope dispatched")
	}
	cm := &corev1.ConfigMap{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: BackupDispatchReceiptName("uid")}, cm); err == nil {
		t.Fatal("failed preflight created a claim")
	}
}

func TestBackupClaimRejectsRevokedApproval(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = hav1alpha1.AddToScheme(scheme)
	op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "uid"},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{Target: hav1alpha1.MigrationTarget{UID: "target"}, ReportID: "report", BackupScopeDigest: "scope",
			Approvals: []hav1alpha1.MigrationApproval{{ID: "backup", Approved: false, TargetUID: "target", Digest: "report", ScopeDigest: "scope"}}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(op).Build()
	store := &ConfigMapBackupDispatchStore{Client: kube, Scheme: scheme, Operation: op}
	claim := BackupDispatchReceipt{OperationUID: "uid", TargetUID: "target", ReportID: "report", ScopeDigest: "scope", Phase: "Dispatching"}
	if ok, err := store.Claim(context.Background(), claim); err == nil || ok {
		t.Fatal("revoked approval claimed")
	}
	cm := &corev1.ConfigMap{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: BackupDispatchReceiptName("uid")}, cm); err == nil {
		t.Fatal("claim was created")
	}
}

func TestDispatchBackupOnceConcurrentClaimHasSingleSender(t *testing.T) {
	include := true
	password := "key"
	source := &concurrentDispatchSource{backupReader: backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
		IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password,
	}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}}
	digest, err := BackupScopeDigest(observeBackupPreflight(context.Background(), source, "token"))
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = hav1alpha1.AddToScheme(scheme)
	op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "uid"},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{Target: hav1alpha1.MigrationTarget{UID: "target"}, ReportID: "report", BackupScopeDigest: digest,
			Approvals: []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true, TargetUID: "target", Digest: "report", ScopeDigest: digest}}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(op).Build()
	store := &ConfigMapBackupDispatchStore{Client: kube, Scheme: scheme, Operation: op}
	intent := BackupDispatchReceipt{OperationUID: "uid", TargetUID: "target", ReportID: "report", ScopeDigest: digest}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = DispatchBackupOnce(context.Background(), store, source, "token", []byte(password), intent)
		}()
	}
	group.Wait()
	if source.calls.Load() != 1 {
		t.Fatalf("send count = %d", source.calls.Load())
	}
}

func TestDispatchBackupOnceHandlesReceiptWriteFailuresWithoutRetry(t *testing.T) {
	include := true
	password := "sensitive-key"
	for _, claimFailure := range []bool{true, false} {
		t.Run(fmt.Sprintf("claimFailure=%t", claimFailure), func(t *testing.T) {
			source := &dispatchSource{backupReader: backupReader{config: &haclient.BackupConfig{CreateBackup: haclient.BackupCreateConfig{
				IncludeDatabase: &include, AgentIDs: []string{"backup.local"}, Password: &password,
			}}, agents: []haclient.BackupAgent{{ID: "backup.local"}}}, id: "backup-id"}
			digest, err := BackupScopeDigest(observeBackupPreflight(context.Background(), source, "token"))
			if err != nil {
				t.Fatal(err)
			}
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = hav1alpha1.AddToScheme(scheme)
			op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "uid"},
				Spec: hav1alpha1.HomeAssistantMigrationSpec{Target: hav1alpha1.MigrationTarget{UID: "target"}, ReportID: "report", BackupScopeDigest: digest,
					Approvals: []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true, TargetUID: "target", Digest: "report", ScopeDigest: digest}}}}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(op).Build()
			store := failingDispatchStore{BackupDispatchStore: &ConfigMapBackupDispatchStore{Client: kube, Scheme: scheme, Operation: op}, claimFailure: claimFailure}
			intent := BackupDispatchReceipt{OperationUID: "uid", TargetUID: "target", ReportID: "report", ScopeDigest: digest}
			_, err = DispatchBackupOnce(context.Background(), store, source, "token", []byte(password), intent)
			if err == nil || strings.Contains(err.Error(), password) {
				t.Fatalf("unexpected error: %v", err)
			}
			want := 1
			if claimFailure {
				want = 0
			}
			if source.calls != want {
				t.Fatalf("calls=%d want=%d", source.calls, want)
			}
			if !claimFailure {
				if _, err := DispatchBackupOnce(context.Background(), store, source, "token", []byte(password), intent); err == nil || source.calls != 1 {
					t.Fatal("unpersisted acceptance was retried")
				}
			}
		})
	}
}

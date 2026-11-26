package migration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	"github.com/przemekhys/homeassistant-operator/test/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type liveBackupSource struct {
	*haclient.Client
	submissions int
}

func (s *liveBackupSource) GenerateBackupWithAutomaticSettings(ctx context.Context, token string) (string, error) {
	s.submissions++
	return s.Client.GenerateBackupWithAutomaticSettings(ctx, token)
}

// Only run against a newly created disposable Container with no user data.
func TestBackupObservationOnDisposableHA(t *testing.T) {
	endpoint := os.Getenv("HA_MIGRATION_DISPOSABLE_URL")
	if endpoint == "" {
		t.Skip("requires explicitly provisioned disposable Home Assistant")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := haclient.NewClient(endpoint)
	for c.CheckAPIReady(ctx) != nil {
		select {
		case <-ctx.Done():
			t.Fatal("disposable HA unavailable")
		case <-time.After(2 * time.Second):
		}
	}
	token, err := c.PerformBootstrap(ctx, "migration-test", "migration-test-password", "Migration Test", "en", &haclient.BootstrapOptions{CreateLongLivedToken: true})
	if err != nil || token == "" {
		t.Fatal("disposable HA bootstrap failed")
	}
	flow, err := c.StartConfigFlow(ctx, token, "local_todo")
	if err != nil {
		t.Fatal("local todo flow unavailable")
	}
	if _, err = c.SubmitConfigFlow(ctx, token, flow.FlowID, map[string]interface{}{"todo_list_name": "Migration Evidence"}); err != nil {
		t.Fatal("local todo creation failed")
	}
	var entryID, entityID string
	for ctx.Err() == nil && entityID == "" {
		entries, e := c.ListConfigEntries(ctx, token)
		if e == nil {
			for _, entry := range entries {
				if entry.Domain == "local_todo" && entry.Title == "Migration Evidence" {
					entryID = entry.EntryID
				}
			}
		}
		if entryID != "" {
			raw, e := c.SendWebSocketCommand(ctx, token, "config/entity_registry/list", nil)
			var entities []struct {
				EntityID      string `json:"entity_id"`
				ConfigEntryID string `json:"config_entry_id"`
				Platform      string `json:"platform"`
			}
			if e == nil && json.Unmarshal(raw, &entities) == nil {
				for _, entity := range entities {
					if entity.ConfigEntryID == entryID && entity.Platform == "local_todo" {
						entityID = entity.EntityID
					}
				}
			}
		}
		if entityID == "" {
			select {
			case <-ctx.Done():
				t.Fatal("local todo identity unavailable")
			case <-time.After(time.Second):
			}
		}
	}
	if err := utils.AddLocalTodoItem(ctx, nil, endpoint, token, entityID, "migration evidence"); err != nil {
		t.Fatal("local todo item creation failed")
	}
	verification := make([]ha.MigrationVerificationItem, 0, 3)
	for _, check := range []struct{ kind, id string }{{"EntityRegistryIdentity", entityID}, {"IntegrationEntryIdentity", entryID}, {"TodoItems", entityID}} {
		projection, e := c.ReadMigrationVerification(ctx, token, check.kind, check.id)
		if e != nil {
			t.Fatalf("%s projection unavailable: %v", check.kind, e)
		}
		digest, e := migrationprotocol.ProjectionDigest(check.kind, check.id, projection)
		if e != nil {
			t.Fatalf("%s projection invalid", check.kind)
		}
		verification = append(verification, ha.MigrationVerificationItem{Kind: check.kind, ID: check.id, ExpectedDigest: digest})
	}
	password := "migration-disposable-backup-key"
	_, err = c.SendWebSocketCommand(ctx, token, "backup/config/update", map[string]interface{}{"create_backup": map[string]interface{}{"agent_ids": []string{"backup.local"}, "include_database": true, "password": password}})
	if err != nil {
		t.Fatal("backup settings unavailable")
	}
	var scope string
	for ctx.Err() == nil {
		scope, err = BackupScopeDigest(observeBackupPreflight(ctx, c, token))
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("protected backup preflight unavailable")
		case <-time.After(time.Second):
		}
	}
	vd, err := migrationprotocol.VerificationDigest(verification)
	if err != nil {
		t.Fatal(err)
	}
	tokenSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default", UID: "token-uid", ResourceVersion: "1"}, Data: map[string][]byte{"token": []byte(token)}}
	keySecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "default", UID: "key-uid", ResourceVersion: "1"}, Data: map[string][]byte{"key": []byte(password)}}
	request := ha.MigrationExecutionRequest{Version: "v1", Stage: "backup", OperationUID: "live-operation", SourceID: endpoint, SourceEndpoint: endpoint, ReportID: "live-report", Target: ha.MigrationTarget{Name: "target", UID: "target-uid"}, ScopeDigest: scope, VerificationDigest: vd, ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, SourceToken: &ha.MigrationSecretIdentity{Name: "token", Key: "token", UID: "token-uid", ResourceVersion: "1"}, RecoveryKey: &ha.MigrationSecretIdentity{Name: "key", Key: "key", UID: "key-uid", ResourceVersion: "1"}}
	request.RequestID, err = migrationprotocol.RequestID(request)
	if err != nil {
		t.Fatal(err)
	}
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "live-operation", Namespace: "default", UID: "live-operation"}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: endpoint, ReportID: "live-report", Target: request.Target, ArchiveStorage: request.ArchiveStorage, Verification: verification, Approvals: []ha.MigrationApproval{{ID: "backup-settings-stable", Approved: true, Digest: "live-report", TargetUID: "target-uid", ScopeDigest: scope}}, ExecutionRequests: []ha.MigrationExecutionRequest{request}, ExecutionAttempts: []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: request.RequestID}}}}
	controller := true
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "archive", Namespace: "default", UID: "archive-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: ha.GroupVersion.String(), Kind: "HomeAssistantMigration", Name: op.Name, UID: op.UID, Controller: &controller}}}}
	scheme := runtime.NewScheme()
	if err := ha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(op, pvc, tokenSecret, keySecret).Build()
	source := &liveBackupSource{Client: c}
	if err := RunBackupWorker(ctx, kube, scheme, "default", op.Name, request.RequestID, source); err != nil {
		t.Fatalf("backup worker submission failed: %v", err)
	}
	store := &workerReceiptStore{operation: op, request: request}
	cm := &corev1.ConfigMap{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: store.name()}, cm); err != nil {
		t.Fatal("accepted receipt unavailable")
	}
	var dispatch BackupDispatchReceipt
	if json.Unmarshal([]byte(cm.Data[backupDispatchReceiptKey]), &dispatch) != nil || dispatch.Phase != "Accepted" || dispatch.RequestID != request.RequestID {
		t.Fatal("accepted receipt invalid")
	}
	jobID := dispatch.JobID
	if err := RunBackupWorker(ctx, kube, scheme, "default", op.Name, request.RequestID, source); err == nil || source.submissions != 1 {
		t.Fatal("repeated backup dispatch permitted")
	}
	for ctx.Err() == nil {
		details, e := c.GetBackupDetails(ctx, token, jobID)
		if e == nil && details.Backup.ID == jobID && details.Backup.Agents["backup.local"].Size > 0 {
			if err := c.CanDecryptBackup(ctx, token, jobID, "backup.local", "wrong-disposable-key"); err == nil {
				t.Fatal("incorrect archive key was accepted")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("backup result unavailable")
		case <-time.After(2 * time.Second):
		}
	}
	dir := t.TempDir()
	err = ArchiveDispatchedBackup(ctx, descriptorUIDClient{kube}, c, "default", op.Name, request.RequestID, dir, time.Second)
	if err != nil {
		t.Fatalf("local archive validation failed: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "backup.tar"))
	if err != nil || info.Size() <= 0 {
		t.Fatal("complete archive not stored")
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadValidatedBackupEvidence(ctx, kube, op, request); err != nil {
		t.Fatal(err)
	}
	t.Logf("disposable HA Container: exact worker job ID matched backup ID; source key and scope verified; streamed and locally validated bytes=%d; this test uses fake Kubernetes, not a Job/PVC", info.Size())
}

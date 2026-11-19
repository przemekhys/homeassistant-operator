package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fakeVerificationHomeAssistant serves the read-only health and WebSocket
// commands used by post-restore verification. It never mutates Home Assistant.
type fakeVerificationHomeAssistant struct {
	server     *httptest.Server
	projection migrationprotocol.TodoItems
}

func newFakeVerificationHomeAssistant(t *testing.T) *fakeVerificationHomeAssistant {
	t.Helper()
	fake := &fakeVerificationHomeAssistant{
		projection: migrationprotocol.TodoItems{
			EntityID: "todo.test",
			Items: []migrationprotocol.TodoItem{
				{UID: "item-1", Summary: "verify restore", Status: "needs_action"},
			},
		},
	}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"API running."}`))
	})
	mux.HandleFunc("/api/websocket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var auth map[string]any
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
		var command map[string]any
		if err := conn.ReadJSON(&command); err != nil {
			return
		}
		var result any
		switch command["type"] {
		case "config/entity_registry/get":
			result = map[string]any{"entity_id": "todo.test", "unique_id": "todo-uid", "platform": "local_todo"}
		case "get_states":
			result = []map[string]any{{"entity_id": "todo.test", "state": "1"}}
		case "todo/item/list":
			items := make([]map[string]any, 0, len(fake.projection.Items))
			for _, item := range fake.projection.Items {
				items = append(items, map[string]any{"uid": item.UID, "summary": item.Summary, "status": item.Status})
			}
			result = map[string]any{"items": items}
		default:
			_ = conn.WriteJSON(map[string]any{"id": 1, "type": "result", "success": false, "error": map[string]any{"code": "unknown_command", "message": "unsupported"}})
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": 1, "type": "result", "success": true, "result": result})
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeVerificationHomeAssistant) expectedDigest(t *testing.T) string {
	t.Helper()
	encoded, err := json.Marshal(f.projection)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := migrationprotocol.ProjectionDigest("TodoItems", "todo.test", encoded)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func (f *fakeVerificationHomeAssistant) clientForHomeAssistant(*hav1.HomeAssistant) *haclient.Client {
	return haclient.NewClient(f.server.URL)
}

// newVerificationFixture builds an accepted operation whose approved verification
// set matches the fake Home Assistant. Restore is only Accepted: HTTP acceptance
// and Pod readiness are not evidence. The reconciler gets a status-enabled client.
func newVerificationFixture(t *testing.T, fake *fakeVerificationHomeAssistant) (*onboardingStageFixture, *migrationBackupQualification) {
	t.Helper()
	ctx := context.Background()
	expected := fake.expectedDigest(t)
	base, op := requestTestObjects(t)
	for _, add := range []func(*runtime.Scheme) error{hav1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, batchv1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(base.Scheme()); err != nil {
			t.Fatal(err)
		}
	}
	op.Finalizers = []string{migrationOperationFinalizer}
	op.Spec.Verification = []hav1alpha1.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: expected}}
	vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
	if err != nil {
		t.Fatal(err)
	}
	for i := range op.Spec.Approvals {
		op.Spec.Approvals[i].VerificationDigest = vd
	}
	archiveDigest := "sha256:" + strings.Repeat("c", 64)
	op.Spec.Archive = &hav1alpha1.MigrationArchive{BackupID: "backup", AgentID: "backup.local", Digest: archiveDigest}
	op.Spec.RestoreScopeDigest = "sha256:" + strings.Repeat("d", 64)
	op.Spec.Approvals = append(op.Spec.Approvals,
		hav1alpha1.MigrationApproval{ID: "restore", Digest: "sha256:" + strings.Repeat("e", 64), TargetUID: op.Spec.Target.UID, ArchiveDigest: archiveDigest, ScopeDigest: op.Spec.RestoreScopeDigest, VerificationDigest: vd, Approved: true},
		hav1alpha1.MigrationApproval{ID: "source-stop", Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID, Approved: true},
	)
	if err := base.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	observation := migrationRequestObservation{ArchiveRef: &hav1alpha1.MigrationObjectReference{Name: "descriptor", UID: "descriptor-uid"}, ArchiveDigest: archiveDigest, ArchivePVCUID: "archive-pvc-uid"}
	backup, err := acceptMigrationRequest(ctx, base, base, op, "backup", observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	upload, err := acceptMigrationRequest(ctx, base, base, op, "upload", observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	restore, err := acceptMigrationRequest(ctx, base, base, op, "restore", observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Spec.ExecutionAttempts = []hav1alpha1.MigrationExecutionAttempt{
		{Stage: "backup", RequestID: backup.RequestID},
		{Stage: "upload", RequestID: upload.RequestID},
		{Stage: "restore", RequestID: restore.RequestID},
	}
	if err := base.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: op.Spec.Target.Name, Namespace: op.Namespace, UID: types.UID(op.Spec.Target.UID)}, Spec: hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true}}}}
	if err := base.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	fixture := &onboardingStageFixture{kube: base, op: op, target: target}
	writeOnboardingReceipt(t, fixture, *upload, "Verified")
	writeOnboardingReceipt(t, fixture, *restore, "Accepted")
	// Rebuild with a status subresource so the reconciler can persist phase.
	var migrations hav1alpha1.HomeAssistantMigrationList
	if err := base.List(ctx, &migrations); err != nil {
		t.Fatal(err)
	}
	var secrets corev1.SecretList
	if err := base.List(ctx, &secrets); err != nil {
		t.Fatal(err)
	}
	var configMaps corev1.ConfigMapList
	if err := base.List(ctx, &configMaps); err != nil {
		t.Fatal(err)
	}
	var targets hav1.HomeAssistantList
	if err := base.List(ctx, &targets); err != nil {
		t.Fatal(err)
	}
	objects := make([]client.Object, 0, len(migrations.Items)+len(secrets.Items)+len(configMaps.Items)+len(targets.Items))
	for i := range migrations.Items {
		objects = append(objects, &migrations.Items[i])
	}
	for i := range secrets.Items {
		objects = append(objects, &secrets.Items[i])
	}
	for i := range configMaps.Items {
		objects = append(objects, &configMaps.Items[i])
	}
	for i := range targets.Items {
		objects = append(objects, &targets.Items[i])
	}
	kube := fakeWithStatus(base.Scheme(), objects...)
	current := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), current); err != nil {
		t.Fatal(err)
	}
	fixture.kube = kube
	fixture.op = current
	qualification := newOnboardingQualification(fixture)
	qualification.clientForHomeAssistant = fake.clientForHomeAssistant
	return fixture, qualification
}

// verificationObserver fixes the backup observation and delegates upload/restore
// to the real qualification, which verifies against the fake Home Assistant.
func verificationObserver(qualification *migrationBackupQualification, op *hav1alpha1.HomeAssistantMigration, backup, upload *hav1alpha1.MigrationExecutionRequest) *verifiedBackupObserver {
	return &verifiedBackupObserver{
		migrationBackupQualification: qualification,
		backup: migrationStageObservation{
			OperationUID: string(op.UID), RequestID: backup.RequestID, Stage: "backup", SourceID: op.Spec.SourceID, TargetUID: op.Spec.Target.UID,
			Phase: "Verified", Verified: true, JobID: "backup-job",
			ArchiveDigest: upload.ArchiveDigest, ArchiveRef: upload.ArchiveRef, ArchivePVCUID: upload.ArchivePVCUID,
			ScopeDigest: backup.ScopeDigest, VerificationDigest: backup.VerificationDigest,
		},
	}
}

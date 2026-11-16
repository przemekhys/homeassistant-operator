package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type loseMigrationUpdateResponse struct{ client.Client }

func (c loseMigrationUpdateResponse) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.Client.Update(ctx, obj, opts...); err != nil {
		return err
	}
	return errors.New("response lost")
}

func requestTestObjects(t *testing.T) (client.Client, *hav1alpha1.HomeAssistantMigration) {
	t.Helper()
	s := runtime.NewScheme()
	if err := hav1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default", UID: "operation"}, Spec: hav1alpha1.HomeAssistantMigrationSpec{
		SourceID: "https://source.example", SourceEndpoint: "https://source.example", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "target-uid"}, ReportID: "report", BackupScopeDigest: "scope",
		ArchiveStorage:       &hav1alpha1.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"},
		SourceTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "token"}, Key: "token"},
		RecoveryKeySecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "key"}, Key: "key"},
		Approvals:            []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true, Digest: "report", ScopeDigest: "scope", TargetUID: "target-uid"}, {ID: "backup-settings-stable", Approved: true, Digest: "report", ScopeDigest: "scope", TargetUID: "target-uid"}},
	}}
	op.Spec.ExecutionProtocol = "v1"
	op.Spec.BackupScopeDigest = "sha256:" + strings.Repeat("a", 64)
	op.Spec.Verification = []hav1alpha1.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: "sha256:" + strings.Repeat("b", 64)}}
	vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
	if err != nil {
		t.Fatal(err)
	}
	for i := range op.Spec.Approvals {
		op.Spec.Approvals[i].ScopeDigest = op.Spec.BackupScopeDigest
		op.Spec.Approvals[i].VerificationDigest = vd
	}
	token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default", UID: "token-uid"}, Data: map[string][]byte{"token": []byte("sensitive-token")}}
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "default", UID: "key-uid"}, Data: map[string][]byte{"key": []byte("sensitive-key")}}
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(op, token, key).Build()
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	return kube, op
}

func TestAcceptBackupRequestRejectsRevokedApproval(t *testing.T) {
	kube, stale := requestTestObjects(t)
	current := stale.DeepCopy()
	current.Spec.Approvals[0].Approved = false
	if err := kube.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptBackupRequest(context.Background(), kube, kube, stale); err == nil {
		t.Fatal("accepted stale approval")
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(stale), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Annotations[migrationRequestBackupAnnotation] != "" {
		t.Fatal("stale request persisted")
	}
}

func TestAcceptBackupRequestRequiresSettingsStabilityApproval(t *testing.T) {
	kube, op := requestTestObjects(t)
	op.Spec.Approvals[1].Approved = false
	if _, err := acceptBackupRequest(context.Background(), kube, kube, op); err == nil {
		t.Fatal("accepted backup without confirmation of stable settings")
	}
}

func TestAcceptBackupRequestRecoversLostResponse(t *testing.T) {
	kube, op := requestTestObjects(t)
	snapshot, err := acceptBackupRequest(context.Background(), loseMigrationUpdateResponse{kube}, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RequestID == "" || snapshot.SourceToken.UID != "token-uid" {
		t.Fatal("missing request identity")
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), stored); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(stored.Spec.ExecutionRequests)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Spec.ExecutionRequests) != 1 || containsRequestSecret(string(encoded)) || stored.Annotations[migrationRequestBackupAnnotation] != "" {
		t.Fatal("missing snapshot or leaked secret")
	}
}

func TestAcceptedRequestSurvivesLaterApprovalsButRejectsChangedInputs(t *testing.T) {
	kube, op := requestTestObjects(t)
	if _, err := acceptBackupRequest(context.Background(), kube, kube, op); err != nil {
		t.Fatal(err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Approvals = append(stored.Spec.Approvals, hav1alpha1.MigrationApproval{ID: "restore", Approved: true})
	if err := migrationprotocol.ValidateAccepted(stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.ExecutionRequests[0].SourceEndpoint = "https://other.example"
	if err := migrationprotocol.ValidateAccepted(stored); err == nil {
		t.Fatal("accepted changed historical endpoint")
	}
}

func TestReconcileRejectsMalformedResolutionWithoutWebhook(t *testing.T) {
	kube, op := requestTestObjects(t)
	op.Spec.Resolutions = []hav1alpha1.MigrationResolution{{Version: "v1", Stage: "backup", RequestID: "id", Decision: "Retry"}}
	if err := kube.Update(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationExecutionInputs(op); err == nil {
		t.Fatal("invalid retry passed reconcile validation")
	}
}

func TestResolutionMustBindAcceptedRequest(t *testing.T) {
	kube, op := requestTestObjects(t)
	snapshot, err := acceptBackupRequest(context.Background(), kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Resolutions = []hav1alpha1.MigrationResolution{{Version: "v1", RequestID: "unrelated", Stage: "backup", Decision: "Abort"}}
	if err := validateMigrationExecutionInputs(stored); err == nil {
		t.Fatal("accepted unrelated resolution")
	}
	stored.Spec.Resolutions[0].RequestID = snapshot.RequestID
	if err := validateMigrationExecutionInputs(stored); err != nil {
		t.Fatal(err)
	}
}

func TestVerificationDigestIsStableAndBindsExpectedData(t *testing.T) {
	items := []hav1alpha1.MigrationVerificationItem{{Kind: "EntityRegistryIdentity", ID: "sensor.a", ExpectedDigest: "sha256:" + strings.Repeat("a", 64)}, {Kind: "EntityRegistryIdentity", ID: "sensor.b", ExpectedDigest: "sha256:" + strings.Repeat("b", 64)}}
	first, err := migrationVerificationDigest(items)
	if err != nil {
		t.Fatal(err)
	}
	items[0], items[1] = items[1], items[0]
	second, err := migrationVerificationDigest(items)
	if err != nil || first != second {
		t.Fatal("verification digest depends on item order")
	}
	items[0].ExpectedDigest = "sha256:" + strings.Repeat("c", 64)
	third, err := migrationVerificationDigest(items)
	if err != nil || third == first {
		t.Fatal("verification digest did not bind expected data")
	}
}

func containsRequestSecret(value string) bool {
	return strings.Contains(value, "sensitive-token") || strings.Contains(value, "sensitive-key")
}

var _ = Describe("Migration execution schema", func() {
	It("rejects removal of a resolution without the admission webhook", func() {
		op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "request-schema-", Namespace: "default"}, Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: "https://source.example", SourceEndpoint: "https://source.example", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		}}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, op) })
		op.Spec.Resolutions = []hav1alpha1.MigrationResolution{{Version: "v1", RequestID: "sha256:" + strings.Repeat("a", 64), Stage: "backup", Decision: "Abort"}}
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		op.Spec.Resolutions = nil
		Expect(k8sClient.Update(ctx, op)).NotTo(Succeed())
	})
	It("rejects an endpoint that does not match the approved source", func() {
		op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "request-endpoint-", Namespace: "default"}, Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: "https://source.example", SourceEndpoint: "https://different.example", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"},
		}}
		Expect(k8sClient.Create(ctx, op)).NotTo(Succeed())
	})
	It("does not accept a stale approval when the API server has observed its revocation", func() {
		token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{GenerateName: "migration-token-", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
		key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{GenerateName: "migration-key-", Namespace: "default"}, Data: map[string][]byte{"key": []byte("test-key")}}
		Expect(k8sClient.Create(ctx, token)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, token) })
		Expect(k8sClient.Create(ctx, key)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, key) })
		op := &hav1alpha1.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "migration-race-", Namespace: "default"}, Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: "https://source.example", SourceEndpoint: "https://source.example", Target: hav1alpha1.MigrationTarget{Name: "target", UID: "uid"}, ReportID: "report", BackupScopeDigest: "scope",
			ArchiveStorage:       &hav1alpha1.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"},
			SourceTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: token.Name}, Key: "token"},
			RecoveryKeySecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: key.Name}, Key: "key"},
			Approvals:            []hav1alpha1.MigrationApproval{{ID: "backup", Approved: true, Digest: "report", ScopeDigest: "scope", TargetUID: "uid"}, {ID: "backup-settings-stable", Approved: true, Digest: "report", ScopeDigest: "scope", TargetUID: "uid"}},
		}}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, op) })
		stale := op.DeepCopy()
		op.Spec.Approvals[0].Approved = false
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		_, err := acceptBackupRequest(ctx, k8sClient, k8sClient, stale)
		Expect(err).To(HaveOccurred())
		stored := &hav1alpha1.HomeAssistantMigration{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), stored)).To(Succeed())
		Expect(stored.Annotations[migrationRequestBackupAnnotation]).To(BeEmpty())
	})
})

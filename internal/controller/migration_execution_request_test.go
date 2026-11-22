package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestExecutionAcceptanceAllStages(t *testing.T) {
	for _, stage := range []string{"backup", "upload", "restore"} {
		t.Run(stage, func(t *testing.T) {
			kube, op := requestTestObjects(t)
			obs := migrationRequestObservation{ArchiveRef: &ha.MigrationObjectReference{Name: "descriptor", UID: "descriptor-uid"}, ArchiveDigest: "sha256:" + strings.Repeat("c", 64), ArchivePVCUID: "pvc-uid"}
			if stage != "backup" {
				op.Spec.Archive = &ha.MigrationArchive{BackupID: "external/backup:1", AgentID: "backup.local", Digest: obs.ArchiveDigest}
				op.Spec.RestoreScopeDigest = "sha256:" + strings.Repeat("d", 64)
				vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
				if err != nil {
					t.Fatal(err)
				}
				op.Spec.Approvals = append(op.Spec.Approvals, ha.MigrationApproval{ID: "restore", Approved: true, Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID, ScopeDigest: op.Spec.RestoreScopeDigest, ArchiveDigest: obs.ArchiveDigest, VerificationDigest: vd}, ha.MigrationApproval{ID: "source-stop", Approved: true, Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID})
				if err := kube.Update(context.Background(), op); err != nil {
					t.Fatal(err)
				}
				if stage == "restore" {
					if _, err := acceptMigrationRequest(context.Background(), kube, kube, op, "upload", obs); err != nil {
						t.Fatal(err)
					}
					if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), op); err != nil {
						t.Fatal(err)
					}
				}
			}
			stale := op.DeepCopy()
			if stage == "backup" {
				stale.Spec.Approvals[0].Approved = false
			} else {
				stale.Spec.Approvals[len(stale.Spec.Approvals)-1].Approved = false
			}
			if _, err := acceptMigrationRequest(context.Background(), kube, kube, stale, stage, obs); err == nil {
				t.Fatal("accepted revoked consent")
			}
			for _, mutate := range []func(*ha.HomeAssistantMigration){
				func(o *ha.HomeAssistantMigration) { o.Spec.Approvals = nil },
				func(o *ha.HomeAssistantMigration) {
					for i := range o.Spec.Approvals {
						if (stage == "backup" && o.Spec.Approvals[i].ID == "backup") || (stage != "backup" && o.Spec.Approvals[i].ID == "restore") {
							o.Spec.Approvals[i].TargetUID = "wrong-target"
						}
					}
				},
				func(o *ha.HomeAssistantMigration) {
					for i := range o.Spec.Approvals {
						if (stage == "backup" && o.Spec.Approvals[i].ID == "backup") || (stage != "backup" && o.Spec.Approvals[i].ID == "restore") {
							o.Spec.Approvals[i].VerificationDigest = "sha256:" + strings.Repeat("f", 64)
						}
					}
				},
			} {
				invalid := op.DeepCopy()
				mutate(invalid)
				if _, err := acceptMigrationRequest(context.Background(), kube, kube, invalid, stage, obs); err == nil {
					t.Fatal("accepted missing or incorrectly bound approval")
				}
			}
			cancelled := op.DeepCopy()
			cancelled.Spec.CancelRequested = true
			if _, err := acceptMigrationRequest(context.Background(), kube, kube, cancelled, stage, obs); err == nil {
				t.Fatal("accepted cancelled operation")
			}
			concurrent := op.DeepCopy()
			if concurrent.Annotations == nil {
				concurrent.Annotations = map[string]string{}
			}
			concurrent.Annotations["test.example/concurrent"] = "change"
			if err := kube.Update(context.Background(), concurrent); err != nil {
				t.Fatal(err)
			}
			if _, err := acceptMigrationRequest(context.Background(), kube, kube, op, stage, obs); err == nil {
				t.Fatal("accepted stale resourceVersion")
			}
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), op); err != nil {
				t.Fatal(err)
			}
			accepted, err := acceptMigrationRequest(context.Background(), loseMigrationUpdateResponse{kube}, kube, op, stage, obs)
			if err != nil {
				t.Fatal(err)
			}
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(op), op); err != nil {
				t.Fatal(err)
			}
			// A new helper invocation after loss of status observes exactly the same record.
			op.Status = ha.HomeAssistantMigrationStatus{}
			op.Spec.SourceTokenSecretRef.Name = "future-token"
			op.Spec.RecoveryKeySecretRef.Name = "future-key"
			again, err := acceptMigrationRequest(context.Background(), kube, kube, op, stage, obs)
			if err != nil || !reflect.DeepEqual(accepted, again) {
				t.Fatal("accepted snapshot changed", err)
			}
			b, err := json.Marshal(again)
			if err != nil {
				t.Fatal(err)
			}
			if containsRequestSecret(string(b)) {
				t.Fatal("snapshot leaked credentials")
			}
			parsed, err := migrationprotocol.ParseRequest(b)
			if err != nil || !reflect.DeepEqual(parsed, again) {
				t.Fatal("request failed round trip", err)
			}
		})
	}
}

func TestCredentialVersionValidation(t *testing.T) {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", UID: "uid", ResourceVersion: "1"}, Data: map[string][]byte{"token": []byte("sensitive-token")}}
	id := &ha.MigrationSecretIdentity{Name: s.Name, UID: string(s.UID), ResourceVersion: s.ResourceVersion, Key: "token"}
	if err := migrationprotocol.ValidateCredentialVersion(id, s); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*corev1.Secret){func(s *corev1.Secret) { s.Name = "other" }, func(s *corev1.Secret) { s.UID = "other" }, func(s *corev1.Secret) { s.ResourceVersion = "2" }, func(s *corev1.Secret) { delete(s.Data, "token") }} {
		copy := s.DeepCopy()
		mutate(copy)
		if err := migrationprotocol.ValidateCredentialVersion(id, copy); err == nil || err.Error() != "CredentialVersionChanged" {
			t.Fatal("credential change not blocked", err)
		}
	}
}

var _ = Describe("Migration protected execution history", func() {
	It("protects snapshots, attempts, protocol, and verification without a webhook across all write methods", func() {
		token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{GenerateName: "protocol-token-", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
		key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{GenerateName: "protocol-key-", Namespace: "default"}, Data: map[string][]byte{"key": []byte("test-key")}}
		Expect(k8sClient.Create(ctx, token)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, token) })
		Expect(k8sClient.Create(ctx, key)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, key) })
		op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "protocol-history-", Namespace: "default"}, Spec: ha.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1", SourceID: "https://source.example", SourceEndpoint: "https://source.example", ReportID: "report", Target: ha.MigrationTarget{Name: "target", UID: "target-uid"}, BackupScopeDigest: "sha256:" + strings.Repeat("a", 64),
			ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, SourceTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: token.Name}, Key: "token"}, RecoveryKeySecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: key.Name}, Key: "key"},
			Verification: []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: "sha256:" + strings.Repeat("b", 64)}},
		}}
		vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
		Expect(err).NotTo(HaveOccurred())
		op.Spec.Approvals = []ha.MigrationApproval{{ID: "backup", Approved: true, Digest: "report", TargetUID: "target-uid", ScopeDigest: op.Spec.BackupScopeDigest, VerificationDigest: vd}, {ID: "backup-settings-stable", Approved: true, Digest: "report", TargetUID: "target-uid", ScopeDigest: op.Spec.BackupScopeDigest}}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, op) })
		r, err := acceptBackupRequest(ctx, k8sClient, k8sClient, op)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), op)).To(Succeed())
		op.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		op.Spec.ExecutionDispatches = []ha.MigrationExecutionAttempt{{Stage: "backup", RequestID: r.RequestID}}
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		// Later consent/generation and metadata do not invalidate accepted records.
		op.Annotations = map[string]string{migrationRequestBackupAnnotation: "legacy-user-evidence"}
		op.Spec.Approvals[0].Approved = false
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		op.Annotations = nil
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		for _, field := range []string{"executionProtocol", "executionRequests", "executionAttempts", "executionDispatches", "verification"} {
			for _, method := range []string{"update", "merge", "json", "apply"} {
				current := &ha.HomeAssistantMigration{}
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), current)).To(Succeed())
				var err error
				switch method {
				case "update":
					switch field {
					case "executionProtocol":
						current.Spec.ExecutionProtocol = ""
					case "executionRequests":
						current.Spec.ExecutionRequests = nil
					case "executionAttempts":
						current.Spec.ExecutionAttempts = nil
					case "executionDispatches":
						current.Spec.ExecutionDispatches = nil
					case "verification":
						current.Spec.Verification = nil
					}
					err = k8sClient.Update(ctx, current)
				case "merge":
					err = k8sClient.Patch(ctx, current, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"`+field+`":null}}`)))
				case "json":
					err = k8sClient.Patch(ctx, current, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/spec/`+field+`"}]`)))
				case "apply":
					var replacement any
					switch field {
					case "executionProtocol":
						replacement = "v2"
					case "executionRequests":
						current.Spec.ExecutionRequests[0].ScopeDigest = "sha256:" + strings.Repeat("f", 64)
						replacement = current.Spec.ExecutionRequests
					case "executionAttempts":
						current.Spec.ExecutionAttempts[0].RequestID = "sha256:" + strings.Repeat("f", 64)
						replacement = current.Spec.ExecutionAttempts
					case "executionDispatches":
						current.Spec.ExecutionDispatches[0].RequestID = "sha256:" + strings.Repeat("f", 64)
						replacement = current.Spec.ExecutionDispatches
					case "verification":
						current.Spec.Verification[0].ExpectedDigest = "sha256:" + strings.Repeat("f", 64)
						replacement = current.Spec.Verification
					}
					payload, marshalErr := json.Marshal(map[string]any{"apiVersion": "ha.homeassistant.io/v1alpha1", "kind": "HomeAssistantMigration", "metadata": map[string]string{"name": op.Name, "namespace": "default"}, "spec": map[string]any{field: replacement}})
					Expect(marshalErr).NotTo(HaveOccurred())
					err = k8sClient.Patch(ctx, current, client.RawPatch(types.ApplyPatchType, payload), client.FieldOwner("protocol-test"), client.ForceOwnership)
				}
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s %s: %v", method, field, err)
			}
		}
		for _, mutate := range []func(*ha.HomeAssistantMigration){
			func(o *ha.HomeAssistantMigration) { o.Spec.ExecutionRequests[0].SourceID = "other" },
			func(o *ha.HomeAssistantMigration) {
				o.Spec.ExecutionAttempts[0].RequestID = "sha256:" + strings.Repeat("f", 64)
			},
			func(o *ha.HomeAssistantMigration) {
				o.Spec.Verification[0].ExpectedDigest = "sha256:" + strings.Repeat("f", 64)
			},
			func(o *ha.HomeAssistantMigration) {
				o.Spec.ExecutionRequests = append(o.Spec.ExecutionRequests, o.Spec.ExecutionRequests[0])
			},
		} {
			current := &ha.HomeAssistantMigration{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), current)).To(Succeed())
			mutate(current)
			Expect(k8sClient.Update(ctx, current)).NotTo(Succeed())
		}
		for _, resolution := range []ha.MigrationResolution{
			{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult"},
			{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "Abort", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: "evidence", UID: "uid", Key: "result"}},
			{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "Secret", Name: "evidence", UID: "uid", Key: "result"}},
			{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: "bad..name", UID: "uid", Key: "result"}},
			{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: "evidence", UID: "", Key: "result"}},
			{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: "evidence", UID: "uid", Key: "../bad"}},
		} {
			current := &ha.HomeAssistantMigration{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), current)).To(Succeed())
			current.Spec.Resolutions = []ha.MigrationResolution{resolution}
			Expect(k8sClient.Update(ctx, current)).NotTo(Succeed())
		}
		current := &ha.HomeAssistantMigration{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), current)).To(Succeed())
		current.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: "backup", RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: "evidence", UID: "uid", Key: "result.json"}}}
		Expect(k8sClient.Update(ctx, current)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), op)).To(Succeed())
		op.Status.Phase = "BackupInProgress"
		Expect(k8sClient.Status().Update(ctx, op)).To(Succeed())
		op.Status = ha.HomeAssistantMigrationStatus{}
		Expect(k8sClient.Status().Update(ctx, op)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), op)).To(Succeed())
		again, err := acceptBackupRequest(ctx, k8sClient, k8sClient, op)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(r))
	})
	It("does not upgrade legacy operations by adding a protocol or deleting annotations", func() {
		op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "protocol-legacy-", Namespace: "default", Annotations: map[string]string{migrationRequestBackupAnnotation: "legacy-evidence"}}, Spec: ha.HomeAssistantMigrationSpec{SourceID: "source", Target: ha.MigrationTarget{Name: "target", UID: "uid"}}}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, op) })
		op.Annotations = nil
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		_, err := acceptBackupRequest(ctx, k8sClient, k8sClient, op)
		Expect(err).To(MatchError("LegacyExecutionProtocol"))
		op.Spec.ExecutionProtocol = "v1"
		Expect(k8sClient.Update(ctx, op)).NotTo(Succeed())
	})
	It("rejects invalid protocol versions and verification sets without a webhook", func() {
		base := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "protocol-invalid-", Namespace: "default"}, Spec: ha.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1", SourceID: "source", Target: ha.MigrationTarget{Name: "target", UID: "uid"},
			Verification: []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: "sha256:" + strings.Repeat("a", 64)}},
		}}
		for _, mutate := range []func(*ha.HomeAssistantMigration){
			func(o *ha.HomeAssistantMigration) { o.Spec.ExecutionProtocol = "v2" },
			func(o *ha.HomeAssistantMigration) { o.Spec.Verification[0].Kind = "Other" },
			func(o *ha.HomeAssistantMigration) { o.Spec.Verification[0].ExpectedDigest = "sha256:invalid" },
			func(o *ha.HomeAssistantMigration) { o.Spec.Verification[0].ID = "" },
			func(o *ha.HomeAssistantMigration) {
				o.Spec.Verification = append(o.Spec.Verification, o.Spec.Verification[0])
			},
		} {
			current := base.DeepCopy()
			mutate(current)
			Expect(k8sClient.Create(ctx, current)).NotTo(Succeed())
		}
		Expect(k8sClient.Create(ctx, base)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, base) })
	})
})

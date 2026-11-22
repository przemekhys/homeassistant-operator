package controller

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var _ = Describe("Migration target policy API", func() {
	It("recovers a lost create response without adopting or rewriting a collision", func() {
		target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{GenerateName: "isolation-target-", Namespace: "default"}, Spec: hav1.HomeAssistantSpec{Version: "stable", Alpha: &hav1.AlphaSpec{MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true}}}}
		Expect(k8sClient.Create(ctx, target)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, target) })
		scope := "sha256:" + strings.Repeat("a", 64)
		archive := "sha256:" + strings.Repeat("c", 64)
		verification := []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: "sha256:" + strings.Repeat("b", 64)}}
		vd, err := migrationprotocol.VerificationDigest(verification)
		Expect(err).NotTo(HaveOccurred())
		op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "isolation-operation-", Namespace: "default"}, Spec: ha.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1", SourceID: "https://source.example", SourceEndpoint: "https://source.example", ReportID: "report", Target: ha.MigrationTarget{Name: target.Name, UID: string(target.UID)}, BackupScopeDigest: scope, RestoreScopeDigest: scope,
			Archive: &ha.MigrationArchive{BackupID: "backup", AgentID: "backup.local", Digest: archive}, ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, Verification: verification,
			Approvals: []ha.MigrationApproval{{ID: "restore", Approved: true, Digest: "report", TargetUID: string(target.UID), ScopeDigest: scope, ArchiveDigest: archive, VerificationDigest: vd}, {ID: "source-stop", Approved: true, Digest: "report", TargetUID: string(target.UID)}},
		}}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, op) })
		obs := migrationRequestObservation{ArchiveRef: &ha.MigrationObjectReference{Name: "descriptor", UID: "descriptor-uid"}, ArchiveDigest: archive, ArchivePVCUID: "pvc-uid"}
		request, err := acceptMigrationRequest(ctx, k8sClient, k8sClient, op, "upload", obs)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), op)).To(Succeed())
		ref, err := createProxyCredentialBundle(ctx, lostSecretCreateResponse{k8sClient}, k8sClient, k8sClient.Scheme(), op, *request, time.Now())
		Expect(err).To(HaveOccurred())
		Expect(ref).To(BeNil())
		bundle := &corev1.Secret{}
		bundleKey := client.ObjectKey{Namespace: op.Namespace, Name: "migration-proxy-" + migrationTargetSelector(string(op.UID))}
		claim := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: bundleKey.Name + "-claim"}, claim)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, claim) })
		Expect(claim.UID).NotTo(BeEmpty())
		Expect(claim.Immutable).NotTo(BeNil())
		Expect(*claim.Immutable).To(BeTrue())
		claim.Data["requestID"] = "replacement"
		Expect(k8sClient.Update(ctx, claim)).NotTo(Succeed())
		Expect(k8sClient.Get(ctx, bundleKey, bundle)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, bundle) })
		Expect(bundle.UID).NotTo(BeEmpty())
		Expect(bundle.Immutable).NotTo(BeNil())
		Expect(*bundle.Immutable).To(BeTrue())
		_, err = createProxyCredentialBundle(ctx, k8sClient, k8sClient, k8sClient.Scheme(), op, *request, time.Now())
		Expect(err).To(HaveOccurred())
		bundle.Data["client.key"] = []byte("replacement")
		Expect(k8sClient.Update(ctx, bundle)).NotTo(Succeed())
		op2 := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "proxy-binding-", Namespace: op.Namespace}, Spec: op.DeepCopy().Spec}
		op2.Spec.ExecutionRequests = nil
		Expect(k8sClient.Create(ctx, op2)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, op2) })
		request2, err := acceptMigrationRequest(ctx, k8sClient, k8sClient, op2, "upload", obs)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op2), op2)).To(Succeed())
		binding, err := createProxyCredentialBundle(ctx, k8sClient, k8sClient, k8sClient.Scheme(), op2, *request2, time.Now())
		Expect(err).NotTo(HaveOccurred())
		bundle2 := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: binding.Bundle.Name, Namespace: op2.Namespace}}
		claim2 := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: binding.Claim.Name, Namespace: op2.Namespace}}
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, bundle2); _ = k8sClient.Delete(ctx, claim2) })
		Expect(bindProxyCredentialBundle(ctx, loseMigrationUpdateResponse{k8sClient}, k8sClient, op2, *binding, time.Now())).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op2), op2)).To(Succeed())
		Expect(op2.Spec.ProxyCredentialBinding).To(Equal(binding))
		changed := op2.DeepCopy()
		changed.Spec.ProxyCredentialBinding.Bundle.UID = "foreign"
		Expect(k8sClient.Update(ctx, changed)).NotTo(Succeed())
		Expect(k8sClient.Patch(ctx, op2, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"proxyCredentialBinding":null}}`)))).NotTo(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op2), op2)).To(Succeed())
		op2.Status = ha.HomeAssistantMigrationStatus{}
		Expect(k8sClient.Status().Update(ctx, op2)).To(Succeed())
		Expect(bindProxyCredentialBundle(ctx, k8sClient, k8sClient, op2, *binding, time.Now())).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(bundle2), bundle2)).To(Succeed())
		bundle2.Labels = map[string]string{"changed": "yes"}
		Expect(k8sClient.Update(ctx, bundle2)).To(Succeed())
		Expect(bindProxyCredentialBundle(ctx, k8sClient, k8sClient, op2, *binding, time.Now())).NotTo(Succeed())
		op3 := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{GenerateName: "proxy-cancel-", Namespace: op.Namespace}, Spec: op.DeepCopy().Spec}
		op3.Spec.ExecutionRequests = nil
		Expect(k8sClient.Create(ctx, op3)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, op3) })
		request3, err := acceptMigrationRequest(ctx, k8sClient, k8sClient, op3, "upload", obs)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op3), op3)).To(Succeed())
		op3.Spec.CancelRequested = true
		Expect(k8sClient.Update(ctx, op3)).To(Succeed())
		cancelled := op3.DeepCopy()
		cancelledBinding := *binding
		cancelledBinding.RequestID = request3.RequestID
		cancelled.Spec.ProxyCredentialBinding = &cancelledBinding
		Expect(k8sClient.Update(ctx, cancelled)).NotTo(Succeed())
		zero := int32(0)
		sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: target.Name, Namespace: target.Namespace}, Spec: appsv1.StatefulSetSpec{ServiceName: target.Name, Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": target.Name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": target.Name, migrationTargetLabel: migrationTargetSelector(string(target.UID))}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "target", Image: "example.invalid/target"}}}}}}
		Expect(controllerutil.SetControllerReference(target, sts, k8sClient.Scheme())).To(Succeed())
		Expect(k8sClient.Create(ctx, sts)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, sts) })
		Expect(ensureStoppedTargetPolicy(ctx, lostPolicyCreateResponse{k8sClient}, k8sClient, k8sClient.Scheme(), op, target, *request)).NotTo(Succeed())
		Expect(ensureStoppedTargetPolicy(ctx, k8sClient, k8sClient, k8sClient.Scheme(), op, target, *request)).To(Succeed())
		policy, err := buildBoundMigrationTargetPolicy(k8sClient.Scheme(), op, target, *request)
		Expect(err).NotTo(HaveOccurred())
		stored := &networkingv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), stored)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, stored) })
		Expect(stored.UID).NotTo(BeEmpty())
		Expect(observeTargetPolicySnapshot(ctx, k8sClient, k8sClient.Scheme(), op, target, *request)).To(Succeed())
		conflict := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{GenerateName: "isolation-conflict-", Namespace: op.Namespace}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": target.Name}}, Ingress: []networkingv1.NetworkPolicyIngressRule{{}}}}
		Expect(k8sClient.Create(ctx, conflict)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, conflict) })
		Expect(observeTargetPolicySnapshot(ctx, k8sClient, k8sClient.Scheme(), op, target, *request)).NotTo(Succeed())
		stored.OwnerReferences = nil
		Expect(k8sClient.Update(ctx, stored)).To(Succeed())
		Expect(ensureStoppedTargetPolicy(ctx, k8sClient, k8sClient, k8sClient.Scheme(), op, target, *request)).NotTo(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(op), op)).To(Succeed())
		op.Spec.Resolutions = []ha.MigrationResolution{{Version: "v1", Stage: "upload", RequestID: request.RequestID, Decision: "Abort"}}
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		attempt := op.DeepCopy()
		bindingCopy := *binding
		bindingCopy.RequestID = request.RequestID
		attempt.Spec.ProxyCredentialBinding = &bindingCopy
		Expect(k8sClient.Update(ctx, attempt)).NotTo(Succeed())
		op.Spec.CancelRequested = true
		Expect(k8sClient.Update(ctx, op)).To(Succeed())
		attempt = op.DeepCopy()
		attempt.Spec.ProxyCredentialBinding = &bindingCopy
		Expect(k8sClient.Update(ctx, attempt)).NotTo(Succeed())
	})
})

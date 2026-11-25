package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type lostPolicyCreateResponse struct{ client.Client }
type lostSecretCreateResponse struct{ client.Client }

func (c lostSecretCreateResponse) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	if _, ok := obj.(*corev1.Secret); ok {
		return fmt.Errorf("simulated lost secret response")
	}
	return nil
}

type proxySecretWriter struct {
	client.Client
	loseResponse bool
	failSecret   bool
}

func (c proxySecretWriter) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok {
		obj.SetUID("proxy-claim-uid")
	}
	if secret, ok := obj.(*corev1.Secret); ok {
		if c.failSecret {
			return fmt.Errorf("simulated rejected secret")
		}
		secret.UID = "proxy-secret-uid"
	}
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	if _, ok := obj.(*corev1.Secret); ok && c.loseResponse {
		return fmt.Errorf("simulated lost response")
	}
	return nil
}

func (c lostPolicyCreateResponse) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	if _, ok := obj.(*networkingv1.NetworkPolicy); ok {
		return fmt.Errorf("simulated lost create response")
	}
	return nil
}

func TestTargetPolicyBindsAcceptedUploadAndOwner(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	for _, add := range []func(*runtime.Scheme) error{hav1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme} {
		if err := add(kube.Scheme()); err != nil {
			t.Fatal(err)
		}
	}
	obs := migrationRequestObservation{ArchiveRef: &ha.MigrationObjectReference{Name: "descriptor", UID: "descriptor-uid"}, ArchiveDigest: "sha256:" + strings.Repeat("c", 64), ArchivePVCUID: "pvc-uid"}
	op.Spec.Archive = &ha.MigrationArchive{BackupID: "backup", AgentID: "backup.local", Digest: obs.ArchiveDigest}
	op.Spec.RestoreScopeDigest = "sha256:" + strings.Repeat("d", 64)
	vd, err := migrationprotocol.VerificationDigest(op.Spec.Verification)
	if err != nil {
		t.Fatal(err)
	}
	op.Spec.Approvals = append(op.Spec.Approvals, ha.MigrationApproval{ID: "restore", Approved: true, Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID, ScopeDigest: op.Spec.RestoreScopeDigest, ArchiveDigest: obs.ArchiveDigest, VerificationDigest: vd}, ha.MigrationApproval{ID: "source-stop", Approved: true, Digest: op.Spec.ReportID, TargetUID: op.Spec.Target.UID})
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	r, err := acceptMigrationRequest(ctx, kube, kube, op, "upload", obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ref, err := createProxyCredentialBundle(ctx, proxySecretWriter{Client: kube}, kube, kube.Scheme(), op, *r, now)
	if err != nil {
		t.Fatal(err)
	}
	bundle := &corev1.Secret{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: ref.Bundle.Name}, bundle); err != nil {
		t.Fatal(err)
	}
	owner := metav1.GetControllerOf(bundle)
	if ref.Bundle.UID != string(bundle.UID) || ref.BundleResourceVersion != bundle.ResourceVersion || bundle.Immutable == nil || !*bundle.Immutable || owner == nil || owner.UID != op.UID {
		t.Fatal("proxy bundle binding unavailable")
	}
	claim := &corev1.ConfigMap{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: ref.Claim.Name}, claim); err != nil {
		t.Fatal(err)
	}
	claimOwner := metav1.GetControllerOf(claim)
	if ref.Claim.UID != string(claim.UID) || claim.Immutable == nil || !*claim.Immutable || claimOwner == nil || claimOwner.UID != op.UID || claim.Data["requestID"] != r.RequestID {
		t.Fatal("proxy credential claim binding unavailable")
	}
	material := &migration.ProxyCredentials{CA: bundle.Data["ca.crt"], ServerCertificate: bundle.Data["server.crt"], ServerKey: bundle.Data["server.key"], WorkerCertificate: bundle.Data["client.crt"], WorkerKey: bundle.Data["client.key"]}
	if err := migration.ValidateProxyCredentials(material, now); err != nil {
		t.Fatal(err)
	}
	if _, err := createProxyCredentialBundle(ctx, proxySecretWriter{Client: kube}, kube, kube.Scheme(), op, *r, now); err == nil {
		t.Fatal("existing credential bundle adopted")
	}
	lost := fake.NewClientBuilder().WithScheme(kube.Scheme()).WithObjects(op.DeepCopy()).Build()
	if _, err := createProxyCredentialBundle(ctx, proxySecretWriter{Client: lost, loseResponse: true}, lost, lost.Scheme(), op, *r, now); err == nil {
		t.Fatal("lost response accepted")
	}
	if _, err := createProxyCredentialBundle(ctx, proxySecretWriter{Client: lost}, lost, lost.Scheme(), op, *r, now); err == nil {
		t.Fatal("lost response bundle adopted")
	}
	failed := fake.NewClientBuilder().WithScheme(kube.Scheme()).WithObjects(op.DeepCopy()).Build()
	if _, err := createProxyCredentialBundle(ctx, proxySecretWriter{Client: failed, failSecret: true}, failed, failed.Scheme(), op, *r, now); err == nil {
		t.Fatal("failed secret creation accepted")
	}
	if _, err := createProxyCredentialBundle(ctx, proxySecretWriter{Client: failed}, failed, failed.Scheme(), op, *r, now); err == nil {
		t.Fatal("failed creation reopened key generation")
	}
	if err := bindProxyCredentialBundle(ctx, loseMigrationUpdateResponse{kube}, kube, op, *ref, now); err != nil {
		t.Fatal("lost binding response not recovered", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op.Spec.ProxyCredentialBinding, ref) {
		t.Fatal("credential binding not persisted")
	}
	if err := bindProxyCredentialBundle(ctx, kube, kube, op, *ref, now); err != nil {
		t.Fatal("idempotent binding rejected", err)
	}
	serverProjection := &corev1.Secret{}
	workerProjection := &corev1.Secret{}
	if kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: proxyServerCredentialName(ref.Bundle.Name)}, serverProjection) != nil || kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: proxyWorkerCredentialName(ref.Bundle.Name)}, workerProjection) != nil || serverProjection.Data["client.key"] != nil || workerProjection.Data["server.key"] != nil || workerProjection.Data["client.key"] == nil {
		t.Fatal("credential projections do not enforce key separation")
	}
	foreignBinding := *ref
	foreignBinding.Bundle.UID = "foreign"
	if err := bindProxyCredentialBundle(ctx, kube, kube, op, foreignBinding, now); err == nil {
		t.Fatal("foreign binding accepted")
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: op.Spec.Target.Name, Namespace: op.Namespace, UID: types.UID(op.Spec.Target.UID)}, Spec: hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true}}}}
	policy, err := buildBoundMigrationTargetPolicy(kube.Scheme(), op, target, *r)
	if err != nil {
		t.Fatal(err)
	}
	if owner := metav1.GetControllerOf(policy); owner == nil || owner.UID != op.UID {
		t.Fatal("policy owner is not migration")
	}
	if policy.Spec.PodSelector.MatchLabels[migrationTargetLabel] != migrationTargetSelector(string(target.UID)) || policy.Spec.Ingress[0].From[0].PodSelector.MatchLabels[migrationRequestLabel] != strings.TrimPrefix(r.RequestID, "sha256:")[:40] {
		t.Fatal("policy not bound to target and request")
	}
	if err := kube.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	yes := true
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: target.Name, Namespace: target.Namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: hav1.GroupVersion.String(), Kind: "HomeAssistant", Name: target.Name, UID: target.UID, Controller: &yes}}}, Spec: appsv1.StatefulSetSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "target", migrationTargetLabel: migrationTargetSelector(string(target.UID))}}}}}
	if err := kube.Create(ctx, sts); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIGRATION_WORKER_IMAGE", "operator:test")
	t.Setenv(migrationWorkerImagePullSecretsEnv, `[{"name":"private-registry"}]`)
	workload := &HomeAssistantReconciler{Client: kube, APIReader: kube, Scheme: kube.Scheme()}
	secured, err := workload.buildStatefulSet(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	proxyFound := false
	for _, container := range secured.Spec.Template.Spec.Containers {
		if container.Name == "migration-onboarding-proxy" && len(container.VolumeMounts) == 1 && container.VolumeMounts[0].Name == "migration-proxy-server" {
			proxyFound = true
		}
	}
	if !proxyFound {
		t.Fatal("target did not receive server-only onboarding proxy")
	}
	if len(secured.Spec.Template.Spec.ImagePullSecrets) != 1 || secured.Spec.Template.Spec.ImagePullSecrets[0].Name != "private-registry" {
		t.Fatal("target proxy image pull secret missing")
	}
	if err := ensureStoppedTargetPolicy(ctx, kube, nil, kube.Scheme(), op, target, *r); err == nil {
		t.Fatal("missing direct reader accepted")
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "target-0", Namespace: target.Namespace, Labels: map[string]string{"app": "target"}}}
	if err := kube.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := ensureStoppedTargetPolicy(ctx, kube, kube, kube.Scheme(), op, target, *r); err == nil {
		t.Fatal("policy created while target pod exists")
	}
	if err := kube.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := ensureStoppedTargetPolicy(ctx, lostPolicyCreateResponse{kube}, kube, kube.Scheme(), op, target, *r); err == nil {
		t.Fatal("lost policy create response accepted")
	}
	if err := ensureStoppedTargetPolicy(ctx, kube, kube, kube.Scheme(), op, target, *r); err != nil {
		t.Fatal("lost policy create response not recovered by observation", err)
	}
	op.Finalizers = append(op.Finalizers, migrationOperationFinalizer)
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	keySecret := &corev1.Secret{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: "key"}, keySecret); err != nil {
		t.Fatal(err)
	}
	createdPolicy := &networkingv1.NetworkPolicy{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(policy), createdPolicy); err != nil {
		t.Fatal(err)
	}
	createdPolicy.UID = "policy-uid" // fake client does not allocate Kubernetes UIDs.
	kube = fake.NewClientBuilder().WithScheme(kube.Scheme()).WithStatusSubresource(op).WithObjects(op, target, sts, keySecret, createdPolicy).Build()
	reconciler := &HomeAssistantMigrationReconciler{Client: kube, APIReader: kube, Scheme: kube.Scheme()}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
		t.Fatal("reconcile isolation preparation", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if op.Status.Phase == "ReadyToExecute" || op.Status.Phase == "Completed" {
		t.Fatal("policy creation falsely enabled execution")
	}
	if err := ensureStoppedTargetPolicy(ctx, kube, kube, kube.Scheme(), op, target, *r); err != nil {
		t.Fatal("idempotent policy rejected", err)
	}
	if err := observeTargetPolicySnapshot(ctx, kube, kube.Scheme(), op, target, *r); err != nil {
		t.Fatal("valid policy snapshot rejected", err)
	}
	conflict := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "conflict", Namespace: op.Namespace}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "target"}}, Ingress: []networkingv1.NetworkPolicyIngressRule{{}}}}
	if err := kube.Create(ctx, conflict); err != nil {
		t.Fatal(err)
	}
	if err := observeTargetPolicySnapshot(ctx, kube, kube.Scheme(), op, target, *r); err == nil {
		t.Fatal("policy selecting another target label was missed")
	}
	if err := kube.Delete(ctx, conflict); err != nil {
		t.Fatal(err)
	}
	stored := &networkingv1.NetworkPolicy{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(policy), stored); err != nil {
		t.Fatal(err)
	}
	stored.OwnerReferences = nil
	if err := kube.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if err := ensureStoppedTargetPolicy(ctx, kube, kube, kube.Scheme(), op, target, *r); err == nil {
		t.Fatal("foreign policy adopted")
	}
	op.Annotations = map[string]string{migrationReceiptAnnotation: "unverified-receipt"}
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if op.Status.Phase != "Unknown" {
		t.Fatalf("policy collision hid unknown execution outcome: %q", op.Status.Phase)
	}
	op.Annotations = nil
	op.Spec.ExecutionAttempts = append(op.Spec.ExecutionAttempts, ha.MigrationExecutionAttempt{Stage: r.Stage, RequestID: r.RequestID})
	op.Spec.ExecutionDispatches = append(op.Spec.ExecutionDispatches, ha.MigrationExecutionAttempt{Stage: r.Stage, RequestID: r.RequestID})
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if op.Status.Phase != "Unknown" {
		t.Fatalf("lost receipt and dispatch marker produced %q", op.Status.Phase)
	}
	target.UID = "other"
	if _, err := buildBoundMigrationTargetPolicy(kube.Scheme(), op, target, *r); err == nil {
		t.Fatal("foreign target accepted")
	}
	target.UID = types.UID(op.Spec.Target.UID)
	foreign := *r
	foreign.RequestID = "sha256:" + strings.Repeat("a", 64)
	if _, err := buildBoundMigrationTargetPolicy(kube.Scheme(), op, target, foreign); err == nil {
		t.Fatal("foreign request accepted")
	}
	cancelled := op.DeepCopy()
	cancelled.Spec.CancelRequested = true
	if _, err := buildBoundMigrationTargetPolicy(kube.Scheme(), cancelled, target, *r); err == nil {
		t.Fatal("cancelled operation accepted")
	}
	restore, err := acceptMigrationRequest(ctx, kube, kube, op, "restore", obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	restorePolicy, err := buildBoundMigrationTargetPolicy(kube.Scheme(), op, target, *restore)
	if err != nil || restorePolicy.Name != policy.Name || restorePolicy.Spec.Ingress[0].From[0].PodSelector.MatchLabels[migrationRequestLabel] == policy.Spec.Ingress[0].From[0].PodSelector.MatchLabels[migrationRequestLabel] {
		t.Fatal("restore policy not bound to distinct request", err)
	}
}

func TestMigrationIsolationRejectsAdditionalIngressPolicies(t *testing.T) {
	if MigrationTargetIsolationPolicy("test", "isolate", "", "worker-label") != nil || MigrationTargetIsolationPolicy("test", "isolate", "target-label", "") != nil {
		t.Fatal("empty security selector accepted")
	}
	want := MigrationTargetIsolationPolicy("test", "isolate", "target-label", "worker-label")
	want.UID = "policy-uid"
	labels := map[string]string{"ha.homeassistant.io/migration-target": "target-label"}
	if !MigrationPolicySetExclusive(want, []networkingv1.NetworkPolicy{*want.DeepCopy()}, labels) {
		t.Fatal("exact policy rejected")
	}
	open := networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "open", Namespace: "test"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, Ingress: []networkingv1.NetworkPolicyIngressRule{{}}}}
	if MigrationPolicySetExclusive(want, []networkingv1.NetworkPolicy{*want, open}, labels) {
		t.Fatal("additive ingress policy accepted")
	}
	open.Spec.PodSelector.MatchLabels = map[string]string{"unrelated": "yes"}
	if !MigrationPolicySetExclusive(want, []networkingv1.NetworkPolicy{*want, open}, labels) {
		t.Fatal("unrelated policy blocked")
	}
	changed := want.DeepCopy()
	changed.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
	if MigrationPolicySetExclusive(want, []networkingv1.NetworkPolicy{*changed}, labels) {
		t.Fatal("modified policy accepted")
	}
	if MigrationPolicySetExclusive(changed, []networkingv1.NetworkPolicy{*changed}, labels) {
		t.Fatal("open policy accepted as expected policy")
	}
	if MigrationPolicySetExclusive(want, nil, labels) {
		t.Fatal("missing policy accepted")
	}
	replaced := want.DeepCopy()
	replaced.UID = "foreign-uid"
	if MigrationPolicySetExclusive(want, []networkingv1.NetworkPolicy{*replaced}, labels) {
		t.Fatal("recreated policy accepted")
	}
	egressOnly := networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "egress-only", Namespace: "test"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}}}
	if !MigrationPolicySetExclusive(want, []networkingv1.NetworkPolicy{*want, egressOnly}, labels) {
		t.Fatal("egress-only policy incorrectly treated as ingress")
	}
}

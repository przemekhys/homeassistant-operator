package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/controller"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	"github.com/przemekhys/homeassistant-operator/test/utils"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TestMigrationRestoreFullOnCluster is the T026 end-to-end gate. It proves the
// full backup -> source stop -> approved restore -> verification -> separate
// resume consent -> manual cutover ordering on a real cluster. It counts actual
// worker Jobs and accepted onboarding receipts per stage, reconstructs the
// isolated reconciler after every accepted request, and asserts that the counts
// remain exactly one. It is cluster-gated and requires a unique worker image.
//
//nolint:gocyclo // One ordered safety scenario with explicit assertions at each boundary.
func TestMigrationRestoreFullOnCluster(t *testing.T) {
	if os.Getenv("HA_MIGRATION_RESTORE_CLUSTER") != "true" {
		t.Skip("set HA_MIGRATION_RESTORE_CLUSTER=true on a disposable cluster")
	}
	image := os.Getenv("MIGRATION_WORKER_IMAGE")
	if image == "" {
		t.Fatal("unique MIGRATION_WORKER_IMAGE is required")
	}
	if os.Getenv("MIGRATION_ISOLATION_ADMISSION_ENABLED") != "true" {
		t.Skip("MIGRATION_ISOLATION_ADMISSION_ENABLED=true is required so the deployed operator starts the target StatefulSet")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		ha.AddToScheme, hav1.AddToScheme, appsv1.AddToScheme,
		batchv1.AddToScheme, corev1.AddToScheme, networkingv1.AddToScheme,
		rbacv1.AddToScheme, authv1.AddToScheme, discoveryv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	ns := "migration-restore-full-" + utils.RandomString(8)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if err := kube.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			cmd := exec.Command("kubectl", "get", "pods,jobs,homeassistants,homeassistantconfigurations,homeassistantmigrations,configmaps", "-n", ns, "-o", "wide")
			out, _ := cmd.CombinedOutput()
			t.Log(string(out))
			cmd = exec.Command("kubectl", "get", "homeassistantmigrations", "-n", ns, "-o", "yaml")
			out, _ = cmd.CombinedOutput()
			t.Log(string(out))
			cmd = exec.Command("kubectl", "get", "homeassistants", "-n", ns, "-o", "yaml")
			out, _ = cmd.CombinedOutput()
			t.Log(string(out))
			cmd = exec.Command("kubectl", "get", "events", "-n", ns, "--sort-by=.lastTimestamp")
			out, _ = cmd.CombinedOutput()
			t.Log(string(out))
			cmd = exec.Command("kubectl", "logs", "-n", ns, "target-0", "-c", "migration-onboarding-proxy", "--tail=40")
			out, _ = cmd.CombinedOutput()
			t.Log(string(out))
			// Worker logs are the only place an outcome-unknown transport error is
			// visible; the receipt deliberately stores no HA response or secret.
			pods, _ := exec.Command("kubectl", "get", "pods", "-n", ns, "-o", "name").CombinedOutput()
			for _, pod := range strings.Fields(string(pods)) {
				logs, _ := exec.Command("kubectl", "logs", "-n", ns, strings.TrimPrefix(pod, "pod/"), "--all-containers", "--tail=40").CombinedOutput()
				t.Logf("%s logs:\n%s", pod, logs)
			}
		}
		backupCleanupNamespace(t, kube, namespace)
	})

	create := func(obj client.Object) {
		t.Helper()
		if err := kube.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Disposable source HA with a counter proxy. The proxy counts actual
	// backup generate/upload/restore commands and proves network isolation.
	create(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "counter", Namespace: ns}, Data: map[string]string{"proxy.py": backupCounterProxy}})
	no := false
	create(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: ns, Labels: map[string]string{"app": "backup-source"}},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no,
			Containers: []corev1.Container{
				{Name: "ha", Image: "ghcr.io/home-assistant/home-assistant:2026.11.0", VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config"}}},
				{Name: "counter", Image: "ghcr.io/home-assistant/home-assistant:2026.11.0", Command: []string{"python", "/test/proxy.py"}, VolumeMounts: []corev1.VolumeMount{{Name: "counter", MountPath: "/test", ReadOnly: true}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8124)}}, PeriodSeconds: 1}},
			},
			Volumes: []corev1.Volume{
				{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "counter", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "counter"}}}},
			},
		},
	})
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "source-isolation", Namespace: ns}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "backup-source"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "ha.homeassistant.io/migration-worker", Operator: metav1.LabelSelectorOpExists}}}}, {PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "trusted-probe"}}}}}}, Egress: []networkingv1.NetworkPolicyEgressRule{}}}
	create(policy)
	service := &corev1.Service{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "kubernetes"}, service); err != nil {
		t.Fatal(err)
	}
	apiPeers := []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: service.Spec.ClusterIP + "/32"}}}
	slices := &discoveryv1.EndpointSliceList{}
	if err := kube.List(ctx, slices, client.InNamespace("default"), client.MatchingLabels{"kubernetes.io/service-name": "kubernetes"}); err != nil {
		t.Fatal(err)
	}
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			for _, address := range endpoint.Addresses {
				apiPeers = append(apiPeers, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: address + "/32"}})
			}
		}
	}
	port := func(n int32) *intstr.IntOrString { v := intstr.FromInt32(n); return &v }
	udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
	// worker-egress is created after the target Service exists, because it must
	// allow the target Service ClusterIP. A PodSelector alone does not match the
	// virtual Service IP on the worker's egress path.
	backupEventually(t, ctx, func() bool {
		p := &corev1.Pod{}
		if kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: "source"}, p) != nil {
			return false
		}
		for _, condition := range p.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, "source proxy ready")
	endpoint, stopSource := backupPortForward(t, ctx, ns, "source", 8124)
	defer stopSource()
	api := haclient.NewClient(endpoint)
	backupEventually(t, ctx, func() bool { return api.CheckAPIReady(ctx) == nil }, "source API ready")
	token, err := api.PerformBootstrap(ctx, "restore-test", "restore-test-password", "Restore Test", "en", &haclient.BootstrapOptions{CreateLongLivedToken: true})
	if err != nil {
		t.Fatal("source bootstrap failed", err)
	}
	projection := backupPrepareVerification(t, ctx, api, endpoint, token)
	password := "disposable-restore-test-key"
	if _, err := api.SendWebSocketCommand(ctx, token, "backup/config/update", map[string]any{"create_backup": map[string]any{"agent_ids": []string{"backup.local"}, "include_database": true, "password": password}}); err != nil {
		t.Fatal("backup settings failed")
	}
	sourcePod := &corev1.Pod{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: "source"}, sourcePod); err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{false, true} {
		name, role := "denied", "untrusted"
		if allowed {
			name, role = "allowed", "trusted-probe"
		}
		script := fmt.Sprintf("import urllib.request,sys,time\nok=False\nfor _ in range(%d):\n try:\n  urllib.request.urlopen('http://%s:8124/counts',timeout=3); ok=True\n except Exception: pass\n if ok: break\n time.sleep(1)\nsys.exit(0 if ok==%s else 1)", map[bool]int{true: 15, false: 1}[allowed], sourcePod.Status.PodIP, map[bool]string{true: "True", false: "False"}[allowed])
		create(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"role": role}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: "ghcr.io/home-assistant/home-assistant:2026.11.0", Command: []string{"python", "-c", script}}}}})
		// The trusted probe retries inside its one-shot container. A retry in the
		// test loop would only observe the same failed Pod and would not wait for
		// the CNI to admit its IP. The negative case remains one attempt because
		// its failure is the expected proof of isolation.
		if allowed {
			backupEventually(t, ctx, func() bool {
				p := &corev1.Pod{}
				_ = kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, p)
				return p.Status.Phase == corev1.PodSucceeded
			}, "network isolation probe allowed")
			continue
		}
		backupEventually(t, ctx, func() bool {
			p := &corev1.Pod{}
			_ = kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, p)
			if p.Status.Phase == corev1.PodFailed {
				t.Fatal("isolation probe failed:", name)
			}
			return p.Status.Phase == corev1.PodSucceeded
		}, "network isolation probe "+name)
	}
	var isolation struct {
		Blocked bool `json:"blocked"`
	}
	backupGetJSON(t, endpoint+"/isolation", &isolation)
	if !isolation.Blocked {
		t.Fatal("source egress policy not enforced")
	}
	internalEndpoint := fmt.Sprintf("http://%s:8124", sourcePod.Status.PodIP)
	report, err := migration.Scan(ctx, api, migration.ScanRequest{SourceID: internalEndpoint, Token: token, Target: migration.Target{Namespace: ns, Name: "target", UID: "target-restore"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backup == nil || report.Backup.Status != "Ready" {
		t.Fatal("protected preflight unavailable")
	}
	scope, err := migration.BackupScopeDigest(report.Backup)
	if err != nil {
		t.Fatal(err)
	}
	vd, err := migrationprotocol.VerificationDigest(projection)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Target HomeAssistant CR. The deployed operator starts its StatefulSet
	// only when migrationRestoreStartAllowed returns true.
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: ns},
		Spec: hav1.HomeAssistantSpec{
			Alpha: &hav1.AlphaSpec{MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true}},
		},
	}
	create(target)
	targetUID := restoreReadHomeAssistantUID(t, ctx, kube, target)
	// The target controller only creates its Service (and StatefulSet) after a
	// matching HomeAssistantConfiguration exists. The restore target must run
	// the HA onboarding proxy before the upload worker can reach it, so the
	// configuration is a required part of the fixture and must be created before
	// waiting for the Service. The configuration controller stays gated during
	// restore, so this file cannot overwrite the restored /config.
	create(&hav1.HomeAssistantConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: target.Name + "-configuration", Namespace: ns},
		Spec: hav1.HomeAssistantConfigurationSpec{
			HomeAssistantRef: hav1.HomeAssistantReference{Name: target.Name},
			Configuration:    "homeassistant:\n  name: Restore Target\ndefault_config:\n",
		},
	})
	// The worker reaches the target through its Service ClusterIP. The CNI
	// evaluates egress policy after Service DNAT, so the target Pod's labels are
	// the reliable peer selector. The ClusterIP IPBlock is kept as well for
	// implementations that evaluate before DNAT. The target's own ingress policy
	// still permits only the pinned worker identity.
	var targetService corev1.Service
	backupEventually(t, ctx, func() bool {
		if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: target.Name}, &targetService); err != nil {
			return false
		}
		return targetService.Spec.ClusterIP != ""
	}, "target service ready")
	targetPodLabels := map[string]string{"ha.homeassistant.io/migration-target": restoreTargetSelector(targetUID)}
	create(&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "worker-egress", Namespace: ns}, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "ha.homeassistant.io/migration-worker", Operator: metav1.LabelSelectorOpExists}}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{To: apiPeers, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: port(443)}, {Protocol: &tcp, Port: port(6443)}}},
			{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "backup-source"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: port(8124)}}},
			{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: targetPodLabels}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: port(8124)}}},
			{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: targetService.Spec.ClusterIP + "/32"}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: port(8124)}}},
			{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: port(53)}, {Protocol: &tcp, Port: port(53)}}},
		},
	}})
	// 3. Backup migration. The target must already exist so its UID can be bound.
	op := &ha.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: ns},
		Spec: ha.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1",
			SourceID:          internalEndpoint,
			SourceEndpoint:    internalEndpoint,
			ReportID:          report.ID,
			Target:            ha.MigrationTarget{Name: "target", UID: targetUID},
			BackupScopeDigest: scope,
			ArchiveStorage:    &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"},
			Verification:      projection,
			SourceTokenSecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "source-token"},
				Key:                  "token",
			},
			RecoveryKeySecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "recovery-key"},
				Key:                  "key",
			},
			Approvals: []ha.MigrationApproval{
				{ID: "backup", Approved: true, Digest: report.ID, TargetUID: targetUID, ScopeDigest: scope, VerificationDigest: vd},
				{ID: "backup-settings-stable", Approved: true, Digest: report.ID, TargetUID: targetUID, ScopeDigest: scope, VerificationDigest: vd},
			},
		},
	}
	create(op)
	create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source-token", Namespace: ns}, Data: map[string][]byte{"token": []byte(token)}})
	create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "recovery-key", Namespace: ns}, Data: map[string][]byte{"key": []byte(password)}})
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: ns}}
	if err := controllerutil.SetControllerReference(op, sa, scheme); err != nil {
		t.Fatal(err)
	}
	create(sa)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: ns}}
	if err := controllerutil.SetControllerReference(op, role, scheme); err != nil {
		t.Fatal(err)
	}
	create(role)
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: ns}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: ns}}}
	if err := controllerutil.SetControllerReference(op, binding, scheme); err != nil {
		t.Fatal(err)
	}
	create(binding)

	// 4. Isolated reconciler. Rebuild it after every accepted request to model a
	// controller restart. The target client is injected later for verification.
	var targetClient *haclient.Client
	options := controller.BackupQualificationOptions{
		Image:          image,
		ServiceAccount: sa.Name,
		ArchiveSize:    resource.MustParse("128Mi"),
		VerifyIsolation: func(ctx context.Context, current *ha.HomeAssistantMigration) error {
			p := &networkingv1.NetworkPolicy{}
			if err := kube.Get(ctx, client.ObjectKeyFromObject(policy), p); err != nil || p.UID != policy.UID || current.UID != op.UID {
				return fmt.Errorf("source isolation changed")
			}
			// The target policy is bound to the upload request. Verify its exact
			// name and owner once the upload request exists.
			for _, request := range current.Spec.ExecutionRequests {
				if request.Stage == "upload" {
					name := "migration-target-" + restoreTargetSelector(targetUID)
					tp := &networkingv1.NetworkPolicy{}
					if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, tp); err != nil {
						return fmt.Errorf("target isolation unavailable")
					}
					owner := metav1.GetControllerOf(tp)
					if owner == nil || owner.UID != current.UID {
						return fmt.Errorf("target isolation owner changed")
					}
					if tp.Spec.PodSelector.MatchLabels["app"] != "" {
						return fmt.Errorf("target isolation selector changed")
					}
				}
			}
			return nil
		},
		ReadBackupResolution: func(ctx context.Context, token string, key []byte, request ha.MigrationExecutionRequest, e migrationprotocol.Evidence) (migration.BackupResolutionObservation, error) {
			return migration.ReadBackupResolution(ctx, api, token, key, request, e)
		},
		ClientForHomeAssistant: func(*hav1.HomeAssistant) *haclient.Client {
			return targetClient
		},
	}
	reconciler, err := controller.NewIsolatedBackupQualificationReconciler(kube, scheme, options)
	if err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}

	// 5. Drive backup to BackupReady. The backup request is accepted first, then
	// the worker RBAC is installed before the worker starts, so the worker can
	// claim its dispatch and read its own receipt.
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, req.NamespacedName, current); err != nil {
			return false
		}
		return restoreHasStage(current, "backup")
	}, "backup request accepted")
	if err := kube.Get(ctx, req.NamespacedName, op); err != nil {
		t.Fatal(err)
	}
	backupRequest := restoreStageRequest(t, op, "backup")
	_, desiredRole, _, err := controller.BuildBackupWorkerRBAC(scheme, op, backupRequest, sa.Name)
	if err != nil {
		t.Fatal(err)
	}
	role.Rules = desiredRole.Rules
	role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{"resolution-evidence"}, Verbs: []string{"get"}})
	if err := kube.Update(ctx, role); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"list", "watch"} {
		sar := &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{User: "system:serviceaccount:" + ns + ":worker", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}, ResourceAttributes: &authv1.ResourceAttributes{Namespace: ns, Verb: verb, Resource: "secrets"}}}
		create(sar)
		if sar.Status.Allowed {
			t.Fatal("worker can enumerate secrets")
		}
	}
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, req.NamespacedName, current); err != nil {
			return false
		}
		return restoreHasDispatch(current, "backup")
	}, "backup worker dispatched")
	restoreWaitForPhase(t, ctx, reconciler, req, "BackupReady", "backup qualified")
	if err := kube.Get(ctx, req.NamespacedName, op); err != nil {
		t.Fatal(err)
	}
	evidence, evidenceCM, err := migration.ReadValidatedBackupEvidence(ctx, kube, op, backupRequest)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Validation == nil || evidence.Validation.Profile != migration.ArchiveValidationProfile {
		t.Fatal("archive validation missing")
	}
	var counts map[string]int
	backupGetJSON(t, endpoint+"/counts", &counts)
	if counts["generate"] != 1 || counts["upload"] != 0 || counts["restore"] != 0 {
		t.Fatal("unexpected HA mutation counts", counts)
	}
	restoreAssertSingleStage(t, ctx, kube, ns, "backup", backupRequest.RequestID)
	_ = evidenceCM

	// 6. Add the restore/source-stop approvals and bind the archive evidence.
	// The source-stop approval is separate from the restore approval and both
	// are bound to the target UID and the archive digest.
	op = readMigration(t, ctx, kube, req.NamespacedName)
	op.Spec.Archive = &ha.MigrationArchive{BackupID: evidence.JobID, AgentID: "backup.local", Digest: evidence.Digest}
	op.Spec.RestoreScopeDigest = scope
	op.Spec.Approvals = append(op.Spec.Approvals,
		ha.MigrationApproval{ID: "restore", Approved: true, Digest: evidence.Digest, TargetUID: targetUID, ArchiveDigest: evidence.Digest, ScopeDigest: scope, VerificationDigest: vd},
		ha.MigrationApproval{ID: "source-stop", Approved: true, Digest: op.Spec.ReportID, TargetUID: targetUID, ArchiveDigest: evidence.Digest, ScopeDigest: scope, VerificationDigest: vd},
	)
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	// Stop only this disposable source. The target is still stopped; the
	// isolated reconciler must not start it before the upload request is accepted.
	if err := kube.Delete(ctx, sourcePod); err != nil {
		t.Fatal(err)
	}

	// 7. Drive upload acceptance. The first reconcile binds the target policy and
	// proxy credentials; the next accepts the upload request. Acceptance is split
	// from worker start so the onboarding Role can be updated before the first
	// POST. Rebuild the reconciler after acceptance and assert one dispatch.
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, req.NamespacedName, current); err != nil {
			return false
		}
		return restoreHasStage(current, "upload")
	}, "upload request accepted")
	if err := kube.Get(ctx, req.NamespacedName, op); err != nil {
		t.Fatal(err)
	}
	uploadRequest := restoreStageRequest(t, op, "upload")
	restoreUpdateOnboardingRBAC(t, ctx, kube, scheme, op, uploadRequest, sa.Name)
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, req.NamespacedName, current); err != nil {
			return false
		}
		return restoreHasDispatch(current, "upload")
	}, "upload worker dispatched")
	restoreAssertSingleStage(t, ctx, kube, ns, "upload", uploadRequest.RequestID)
	reconciler, err = controller.NewIsolatedBackupQualificationReconciler(kube, scheme, options)
	if err != nil {
		t.Fatal(err)
	}
	restoreAssertSingleStage(t, ctx, kube, ns, "upload", uploadRequest.RequestID)

	// 8. Wait for the target pod and port-forward its HA API. The target proxy is
	// also counted from its logs to prove exactly one upload POST. The target pod
	// only starts after the source is stopped and the upload request is accepted.
	backupEventually(t, ctx, func() bool {
		p := &corev1.Pod{}
		if kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: "target-0"}, p) != nil {
			return false
		}
		for _, condition := range p.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, "target pod ready")
	targetEndpoint, stopTarget := backupPortForwardResilient(t, ctx, ns, "target-0", 8123)
	defer stopTarget()
	targetClient = haclient.NewClient(targetEndpoint)
	backupEventually(t, ctx, func() bool { return targetClient.CheckAPIReady(ctx) == nil }, "target API ready")

	// 9. Drive restore acceptance. The restore request is accepted only after the
	// upload request is verified and the source is stopped. Split acceptance from
	// worker start so the restore Role is installed first. Rebuild and assert.
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, req.NamespacedName, current); err != nil {
			return false
		}
		return restoreHasStage(current, "restore")
	}, "restore request accepted")
	if err := kube.Get(ctx, req.NamespacedName, op); err != nil {
		t.Fatal(err)
	}
	restoreRequest := restoreStageRequest(t, op, "restore")
	restoreUpdateOnboardingRBAC(t, ctx, kube, scheme, op, restoreRequest, sa.Name)
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kube.Get(ctx, req.NamespacedName, current); err != nil {
			return false
		}
		return restoreHasDispatch(current, "restore")
	}, "restore worker dispatched")
	restoreAssertSingleStage(t, ctx, kube, ns, "restore", restoreRequest.RequestID)
	reconciler, err = controller.NewIsolatedBackupQualificationReconciler(kube, scheme, options)
	if err != nil {
		t.Fatal(err)
	}
	restoreAssertSingleStage(t, ctx, kube, ns, "restore", restoreRequest.RequestID)

	// 10. Verification runs against the port-forwarded target. The phase must
	// reach AwaitingResumeApproval without resume consent. Capture the verified
	// result digest from the same observation: the deployed controller may
	// overwrite the condition immediately afterwards. On timeout, run the
	// read-only verification directly so the actual mismatch is visible.
	resultDigest := restoreWaitForVerifiedPhase(t, ctx, reconciler, req, op, targetClient, kube, ns)
	restoreAssertSingleStage(t, ctx, kube, ns, "upload", uploadRequest.RequestID)
	restoreAssertSingleStage(t, ctx, kube, ns, "restore", restoreRequest.RequestID)
	restoreAssertProxyLogs(t, ctx, ns, "POST /api/onboarding/backup/upload")
	restoreAssertProxyLogs(t, ctx, ns, "POST /api/onboarding/backup/restore")

	// 11. Resume consent is a separate approval bound to the verified result
	// digest. Reconcile without it first and prove the phase does not advance.
	restoreAssertPhaseStays(t, ctx, reconciler, req, "AwaitingResumeApproval", "resume approval not yet granted")
	op = readMigration(t, ctx, kube, req.NamespacedName)
	op.Spec.Approvals = append(op.Spec.Approvals, ha.MigrationApproval{ID: "resume", Approved: true, Digest: resultDigest, TargetUID: targetUID, ArchiveDigest: evidence.Digest, ScopeDigest: scope, VerificationDigest: vd})
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	restoreWaitForPhase(t, ctx, reconciler, req, "AwaitingManualCutover", "resume approved")

	// 12. Manual cutover is a separate approval. Without it the phase must remain
	// AwaitingManualCutover and must not become Completed.
	restoreAssertPhaseStays(t, ctx, reconciler, req, "AwaitingManualCutover", "manual cutover not yet granted")
	op = readMigration(t, ctx, kube, req.NamespacedName)
	op.Spec.Approvals = append(op.Spec.Approvals, ha.MigrationApproval{ID: "manual-cutover", Approved: true, Digest: resultDigest, TargetUID: targetUID, ArchiveDigest: evidence.Digest, ScopeDigest: scope, VerificationDigest: vd})
	if err := kube.Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	restoreWaitForPhase(t, ctx, reconciler, req, "Completed", "manual cutover approved")
	restoreAssertSingleStage(t, ctx, kube, ns, "backup", backupRequest.RequestID)
	restoreAssertSingleStage(t, ctx, kube, ns, "upload", uploadRequest.RequestID)
	restoreAssertSingleStage(t, ctx, kube, ns, "restore", restoreRequest.RequestID)
}

// restoreReadHomeAssistantUID reads the UID assigned by the API server.
func restoreReadHomeAssistantUID(t *testing.T, ctx context.Context, kube client.Client, target *hav1.HomeAssistant) string {
	t.Helper()
	current := &hav1.HomeAssistant{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(target), current); err != nil {
		t.Fatal(err)
	}
	if current.UID == "" {
		t.Fatal("target HomeAssistant UID is empty")
	}
	return string(current.UID)
}

// restoreStageRequest returns the accepted request for a stage.
func restoreStageRequest(t *testing.T, op *ha.HomeAssistantMigration, stage string) ha.MigrationExecutionRequest {
	t.Helper()
	for _, request := range op.Spec.ExecutionRequests {
		if request.Stage == stage {
			return request
		}
	}
	t.Fatalf("%s request not accepted", stage)
	return ha.MigrationExecutionRequest{}
}

// restoreTargetSelector mirrors the production deterministic selector. It is
// computed only to locate the target NetworkPolicy by name.
func restoreTargetSelector(uid string) string {
	digest := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(digest[:20])
}

// restoreWaitForPhase drives the isolated reconciler until the requested phase
// is observed. It tolerates transient Blocked/Unknown statuses written by the
// deployed controller, but fails on a non-retryable worker failure.
func restoreWaitForPhase(t *testing.T, ctx context.Context, reconciler *controller.HomeAssistantMigrationReconciler, req ctrl.Request, want, description string) {
	t.Helper()
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kubeGet(ctx, reconciler, req, current); err != nil {
			return false
		}
		if current.Status.Phase == "Blocked" {
			for _, condition := range current.Status.Conditions {
				switch condition.Reason {
				case "ExecutionAttemptNotStarted", "SourceStopUnverified", "RestoreApprovalRequired",
					"OnboardingIsolationUnavailable", "OnboardingIsolationPreparationUnavailable",
					"OnboardingCredentialBindingUnavailable", "OnboardingStartPreconditionUnmet",
					"BackupRequestNotAccepted":
					// transient preparation states
				default:
					t.Fatal("migration blocked:", condition.Reason)
				}
			}
		}
		return current.Status.Phase == want
	}, description)
}

// backupPortForwardResilient keeps a fixed local port forwarded to the pod.
// The target Home Assistant restarts its core after a restore, which closes the
// forwarded port and makes a one-shot `kubectl port-forward` exit. Verification
// retries against the same base URL, so the forwarder is restarted until the
// parent context is cancelled.
func backupPortForwardResilient(t *testing.T, ctx context.Context, ns, pod string, remote int) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			cmd := exec.CommandContext(child, "kubectl", "port-forward", "-n", ns, "pod/"+pod, fmt.Sprintf("%d:%d", port, remote), "--address=127.0.0.1")
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			if err := cmd.Start(); err != nil {
				return
			}
			_ = cmd.Wait()
			select {
			case <-child.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()
	return fmt.Sprintf("http://127.0.0.1:%d", port), func() { cancel(); <-done }
}

// restoreWaitForVerifiedPhase drives the isolated reconciler until the restore
// verification releases the resume gate. On timeout it runs the same read-only
// verification directly against the target so the concrete failure (access,
// transport or projection mismatch) is reported instead of only the phase that
// never arrived. The target port-forward and the source token Secret are still
// available at that point.
func restoreWaitForVerifiedPhase(t *testing.T, ctx context.Context, reconciler *controller.HomeAssistantMigrationReconciler, req ctrl.Request, op *ha.HomeAssistantMigration, targetClient *haclient.Client, kube client.Client, ns string) string {
	t.Helper()
	for {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kubeGet(ctx, reconciler, req, current); err == nil {
			if current.Status.Phase == "AwaitingResumeApproval" {
				if digest := restoreResultDigest(current); digest != "" {
					return digest
				}
			}
			if current.Status.Phase == "Blocked" {
				for _, condition := range current.Status.Conditions {
					switch condition.Reason {
					case "ExecutionAttemptNotStarted", "SourceStopUnverified", "RestoreApprovalRequired",
						"OnboardingIsolationUnavailable", "OnboardingIsolationPreparationUnavailable",
						"OnboardingCredentialBindingUnavailable", "OnboardingStartPreconditionUnmet",
						"BackupRequestNotAccepted":
						// transient preparation states
					default:
						t.Fatal("migration blocked:", condition.Reason)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			restoreDiagnoseVerification(t, op, targetClient, kube, ns)
			t.Fatal("timeout: restore verified")
		case <-time.After(time.Second):
		}
	}
}

// restoreResultDigest extracts the verified result digest from the isolated
// reconciler's AwaitingResumeApproval condition. The deployed controller may
// overwrite the condition right after the write, so the wait captures it while
// the observed phase still belongs to the isolated reconciler.
func restoreResultDigest(current *ha.HomeAssistantMigration) string {
	const digestPrefix = "awaiting resume approval with digest "
	const digestSuffix = " matching verified result"
	for _, condition := range current.Status.Conditions {
		if condition.Type == "Progressing" && strings.HasPrefix(condition.Message, digestPrefix) && strings.HasSuffix(condition.Message, digestSuffix) {
			return strings.TrimSuffix(strings.TrimPrefix(condition.Message, digestPrefix), digestSuffix)
		}
	}
	return ""
}

// restoreDiagnoseVerification reproduces the operator's read-only verification
// against the target and logs the exact failure. It never mutates Home Assistant.
func restoreDiagnoseVerification(t *testing.T, op *ha.HomeAssistantMigration, targetClient *haclient.Client, kube client.Client, ns string) {
	t.Helper()
	_ = targetClient
	diagCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// The original port-forward is cancelled with the test context, so open a
	// fresh one for the diagnosis.
	endpoint, stop := backupPortForwardResilient(t, diagCtx, ns, "target-0", 8123)
	defer stop()
	targetClient = haclient.NewClient(endpoint)
	secret := &corev1.Secret{}
	if err := kube.Get(diagCtx, client.ObjectKey{Namespace: ns, Name: "source-token"}, secret); err != nil {
		t.Log("diagnosis: source token secret unavailable:", err)
		return
	}
	token := string(secret.Data["token"])
	if token == "" {
		t.Log("diagnosis: source token empty")
		return
	}
	if err := targetClient.CheckHealthWithToken(diagCtx, token); err != nil {
		t.Log("diagnosis: target health with operator token failed:", err)
	}
	for _, item := range op.Spec.Verification {
		data, err := targetClient.ReadMigrationVerification(diagCtx, token, item.Kind, item.ID)
		if err != nil {
			t.Logf("diagnosis: read %s/%s failed: %v", item.Kind, item.ID, err)
			continue
		}
		digest, err := migrationprotocol.ProjectionDigest(item.Kind, item.ID, data)
		if err != nil {
			t.Logf("diagnosis: digest %s/%s failed: %v", item.Kind, item.ID, err)
			continue
		}
		t.Logf("diagnosis: %s/%s observed=%s expected=%s match=%t", item.Kind, item.ID, digest, item.ExpectedDigest, digest == item.ExpectedDigest)
	}
}

// restoreAssertPhaseStays drives the reconciler and proves the phase remains the
// expected value. It is used to prove a consent gate is not bypassed by a
// reconcile or a controller restart. The deployed controller writes a
// conservative Unknown fallback while the isolated reconciler owns the state
// machine; that is not a consent-gate advancement, so it is retried.
func restoreAssertPhaseStays(t *testing.T, ctx context.Context, reconciler *controller.HomeAssistantMigrationReconciler, req ctrl.Request, want, description string) {
	t.Helper()
	backupEventually(t, ctx, func() bool {
		reconcileMigration(t, ctx, reconciler, req)
		current := &ha.HomeAssistantMigration{}
		if err := kubeGet(ctx, reconciler, req, current); err != nil {
			return false
		}
		switch current.Status.Phase {
		case want:
			return true
		case "Unknown":
			return false
		case "Blocked":
			for _, condition := range current.Status.Conditions {
				switch condition.Reason {
				case "ExecutionAttemptNotStarted", "SourceStopUnverified", "RestoreApprovalRequired",
					"OnboardingIsolationUnavailable", "OnboardingIsolationPreparationUnavailable",
					"OnboardingCredentialBindingUnavailable", "OnboardingStartPreconditionUnmet",
					"BackupRequestNotAccepted":
					return false
				default:
					t.Fatal("migration blocked:", condition.Reason)
				}
			}
			return false
		case "AwaitingManualCutover", "Completed":
			if want == "AwaitingResumeApproval" || want == "AwaitingManualCutover" {
				t.Fatalf("%s: phase advanced to %q without the required consent", description, current.Status.Phase)
			}
			return false
		default:
			return false
		}
	}, description)
}

// reconcileMigration runs one reconcile, tolerating a conflict caused by the
// deployed controller updating the same object concurrently. The next retry
// reads the latest object; other errors are fatal.
func reconcileMigration(t *testing.T, ctx context.Context, reconciler *controller.HomeAssistantMigrationReconciler, req ctrl.Request) {
	t.Helper()
	if _, err := reconciler.Reconcile(ctx, req); err != nil && !apierrors.IsConflict(err) {
		t.Fatal(err)
	}
}

// kubeGet uses the reconciler's client to read the migration.
func kubeGet(ctx context.Context, reconciler *controller.HomeAssistantMigrationReconciler, req ctrl.Request, current *ha.HomeAssistantMigration) error {
	return reconciler.Get(ctx, req.NamespacedName, current)
}

// restoreAssertSingleStage proves exactly one worker Job and one accepted
// receipt exist for the request. It counts actual objects, not only absence of
// duplicates.
func restoreAssertSingleStage(t *testing.T, ctx context.Context, kube client.Client, ns, stage, requestID string) {
	t.Helper()
	op := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: "backup"}, op); err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	for _, marker := range op.Spec.ExecutionDispatches {
		if marker.Stage == stage && marker.RequestID == requestID {
			dispatches++
		}
	}
	if dispatches != 1 {
		t.Fatalf("%s dispatch marker count=%d, want 1", stage, dispatches)
	}
	jobs := &batchv1.JobList{}
	label := "ha.homeassistant.io/migration-worker"
	if stage == "upload" || stage == "restore" {
		label = "ha.homeassistant.io/migration-onboarding-request"
	}
	if err := kube.List(ctx, jobs, client.InNamespace(ns), client.MatchingLabels{label: strings.TrimPrefix(requestID, "sha256:")[:40]}); err != nil {
		t.Fatal(err)
	}
	// Read-only observation and resolution continuations are allowed and must
	// not be mistaken for a second dispatch. Only the one-shot dispatch Job may
	// exist exactly once.
	jobDispatches := 0
	for _, job := range jobs.Items {
		if strings.HasPrefix(job.Name, "migration-observe-") || strings.HasPrefix(job.Name, "migration-resolve-") {
			continue
		}
		jobDispatches++
	}
	if jobDispatches != 1 {
		t.Fatalf("%s dispatch Job count=%d, want 1", stage, jobDispatches)
	}
	receiptName := migration.WorkerReceiptName(requestID)
	if stage == "upload" || stage == "restore" {
		receiptName = migration.OnboardingReceiptName(requestID)
	}
	receipt := &corev1.ConfigMap{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: receiptName}, receipt); err != nil {
		t.Fatalf("%s receipt unavailable: %v", stage, err)
	}
	var phase string
	if stage == "backup" {
		var dispatch migration.BackupDispatchReceipt
		if err := json.Unmarshal([]byte(receipt.Data["receipt.json"]), &dispatch); err != nil {
			t.Fatal(err)
		}
		phase = dispatch.Phase
	} else {
		var onboarding migration.OnboardingReceipt
		if err := json.Unmarshal([]byte(receipt.Data["receipt.json"]), &onboarding); err != nil {
			t.Fatal(err)
		}
		phase = onboarding.Phase
	}
	if phase != "Accepted" && phase != "Verified" {
		t.Fatalf("%s receipt phase=%q, want Accepted or Verified", stage, phase)
	}
}

// restoreAssertProxyLogs proves exactly one production proxy request log line.
// The count is an additional invariant: a controller restart must not produce a
// second POST. The expected count is always one, so it is not a parameter.
func restoreAssertProxyLogs(t *testing.T, ctx context.Context, ns, needle string) {
	t.Helper()
	const want = 1
	cmd := exec.CommandContext(ctx, "kubectl", "logs", "-n", ns, "target-0", "-c", "migration-onboarding-proxy")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read proxy logs: %v", err)
	}
	if got := strings.Count(string(out), needle); got != want {
		t.Fatalf("proxy log %q count=%d, want %d\n%s", needle, got, want, out)
	}
}

// readMigration is defined in migration_restore_test.go and is reused here.

// restoreHasStage reports whether the migration has accepted a request for the
// stage. It is used to split acceptance from worker start so the onboarding
// worker Role can be updated before the first POST.
func restoreHasStage(op *ha.HomeAssistantMigration, stage string) bool {
	for _, request := range op.Spec.ExecutionRequests {
		if request.Stage == stage {
			return true
		}
	}
	return false
}

// restoreHasDispatch reports whether the worker has claimed its one-shot POST
// permit for the stage.
func restoreHasDispatch(op *ha.HomeAssistantMigration, stage string) bool {
	for _, dispatch := range op.Spec.ExecutionDispatches {
		if dispatch.Stage == stage {
			return true
		}
	}
	return false
}

// restoreUpdateOnboardingRBAC replaces the worker Role rules with the exact
// onboarding permissions for the accepted request. It runs between acceptance
// and worker start so the worker's first POST has the required access.
func restoreUpdateOnboardingRBAC(t *testing.T, ctx context.Context, kube client.Client, scheme *runtime.Scheme, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest, saName string) {
	t.Helper()
	_, desiredRole, _, err := controller.BuildOnboardingWorkerRBAC(scheme, op, request, saName)
	if err != nil {
		t.Fatal(err)
	}
	current := &rbacv1.Role{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: saName}, current); err != nil {
		t.Fatal(err)
	}
	current.Rules = desiredRole.Rules
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
}

// Ensure optional references remain in use when the cluster gate is skipped.
var (
	_ = apierrors.IsNotFound
	_ = metav1.GetControllerOf
)

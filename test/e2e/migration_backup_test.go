package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/controller"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	"github.com/przemekhys/homeassistant-operator/test/utils"
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

// This counter proxy is test instrumentation, not a production service. It
// forwards authenticated frames without storing them, counting only command
// names to distinguish actual requests from a coincidentally stable inventory.
const backupCounterProxy = `
import asyncio,json,os
from aiohttp import web,ClientSession,ClientTimeout,WSMsgType
counts={"generate":0,"upload":0,"restore":0}
async def handler(req):
    if req.path=="/counts": return web.json_response(counts)
    if req.path=="/isolation":
        try:
            r,w=await asyncio.wait_for(asyncio.open_connection(os.environ["KUBERNETES_SERVICE_HOST"],443),3)
            w.close()
            return web.json_response({"blocked":False})
        except (TimeoutError,OSError): return web.json_response({"blocked":True})
    async with ClientSession(timeout=ClientTimeout(total=300),auto_decompress=False) as s:
        url="http://127.0.0.1:8123"+str(req.rel_url)
        if req.path=="/api/websocket":
            ws=web.WebSocketResponse(); await ws.prepare(req)
            async with s.ws_connect(url) as upstream:
                async def relay(a,b,record=False):
                    async for msg in a:
                        if msg.type==WSMsgType.TEXT:
                            if record:
                                try:
                                    command=json.loads(msg.data).get("type","")
                                    if command=="backup/generate_with_automatic_settings": counts["generate"]+=1
                                except (ValueError,AttributeError): pass
                            await b.send_str(msg.data)
                        elif msg.type==WSMsgType.BINARY: await b.send_bytes(msg.data)
                tasks=[asyncio.create_task(relay(ws,upstream,True)),asyncio.create_task(relay(upstream,ws))]
                done,pending=await asyncio.wait(tasks,return_when=asyncio.FIRST_COMPLETED)
                for task in pending: task.cancel()
                await asyncio.gather(*tasks,return_exceptions=True)
            return ws
        if "upload" in req.path and req.method=="POST": counts["upload"]+=1
        if "restore" in req.path and req.method=="POST": counts["restore"]+=1
        headers={k:v for k,v in req.headers.items() if k.lower() not in ("host","content-length","connection")}
        async with s.request(req.method,url,headers=headers,data=await req.read()) as up:
            response=web.StreamResponse(status=up.status,headers={k:v for k,v in up.headers.items() if k.lower() in ("content-type","content-length","content-encoding")})
            await response.prepare(req)
            async for chunk in up.content.iter_chunked(65536): await response.write(chunk)
            await response.write_eof()
            return response
app=web.Application();app.router.add_route("*","/{path:.*}",handler)
web.run_app(app,host="0.0.0.0",port=8124,print=None,access_log=None)
`

//nolint:gocyclo // One ordered safety scenario with explicit assertions at each boundary.
func TestMigrationBackupOnCluster(t *testing.T) {
	if os.Getenv("HA_MIGRATION_BACKUP_CLUSTER") != "true" {
		t.Skip("requires explicitly isolated backup qualification cluster")
	}
	image := os.Getenv("MIGRATION_WORKER_IMAGE")
	if image == "" {
		t.Fatal("unique MIGRATION_WORKER_IMAGE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{ha.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme, networkingv1.AddToScheme, rbacv1.AddToScheme, authv1.AddToScheme, discoveryv1.AddToScheme} {
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
	ns := "migration-backup-" + utils.RandomString(8)
	create := func(obj client.Object) {
		t.Helper()
		if err := kube.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	testNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	create(testNamespace)
	t.Cleanup(func() {
		if t.Failed() {
			cmd := exec.Command("kubectl", "get", "pods,jobs,homeassistantmigrations,configmaps", "-n", ns, "-o", "wide")
			out, _ := cmd.CombinedOutput()
			t.Log(string(out))
			cmd = exec.Command("kubectl", "logs", "-n", ns, "source", "-c", "counter", "--tail=15")
			out, _ = cmd.CombinedOutput()
			t.Log(string(out))
			cmd = exec.Command("kubectl", "logs", "-n", ns, "-l", "ha.homeassistant.io/migration-worker", "--all-containers", "--tail=30")
			out, _ = cmd.CombinedOutput()
			t.Log(string(out))
		}
		if t.Failed() && os.Getenv("MIGRATION_BACKUP_KEEP_FAILED") == "true" {
			t.Log("retained isolated test namespace:", ns)
			return
		}
		backupCleanupNamespace(t, kube, testNamespace)
	})
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
	create(&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "worker-egress", Namespace: ns}, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "ha.homeassistant.io/migration-worker", Operator: metav1.LabelSelectorOpExists}}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{To: apiPeers, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: port(443)}, {Protocol: &tcp, Port: port(6443)}}},
			{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "backup-source"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: port(8124)}}},
			{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: port(53)}, {Protocol: &tcp, Port: port(53)}}},
		},
	}})
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
	endpoint, stop := backupPortForward(t, ctx, ns, "source", 8124)
	defer stop()
	api := haclient.NewClient(endpoint)
	backupEventually(t, ctx, func() bool { return api.CheckAPIReady(ctx) == nil }, "source API ready")
	token, err := api.PerformBootstrap(ctx, "backup-test", "backup-test-password", "Backup Test", "en", &haclient.BootstrapOptions{CreateLongLivedToken: true})
	if err != nil {
		t.Fatal("source bootstrap failed", err)
	}
	projection := backupPrepareVerification(t, ctx, api, endpoint, token)
	password := "disposable-qualification-backup-key"
	if _, err := api.SendWebSocketCommand(ctx, token, "backup/config/update", map[string]any{"create_backup": map[string]any{"agent_ids": []string{"backup.local"}, "include_database": true, "password": password}}); err != nil {
		t.Fatal("backup settings failed")
	}
	// Prove CNI enforcement with both positive and negative controls, rather
	// than treating the presence of a NetworkPolicy as evidence of isolation.
	sourcePod := &corev1.Pod{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: "source"}, sourcePod); err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{false, true} {
		name, role := "denied", "untrusted"
		if allowed {
			name, role = "allowed", "trusted-probe"
		}
		script := fmt.Sprintf("import urllib.request,sys\ntry:\n urllib.request.urlopen('http://%s:8124/counts',timeout=3); ok=True\nexcept Exception: ok=False\nsys.exit(0 if ok==%s else 1)", sourcePod.Status.PodIP, map[bool]string{true: "True", false: "False"}[allowed])
		create(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"role": role}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: "ghcr.io/home-assistant/home-assistant:2026.11.0", Command: []string{"python", "-c", script}}}}})
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
	report, err := migration.Scan(ctx, api, migration.ScanRequest{SourceID: internalEndpoint, Token: token, Target: migration.Target{Namespace: ns, Name: "target-never-started", UID: "target-qualification"}})
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
	op := &ha.HomeAssistantMigration{ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: ns}, Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", SourceID: internalEndpoint, SourceEndpoint: internalEndpoint, ReportID: report.ID, Target: ha.MigrationTarget{Name: "target-never-started", UID: "target-qualification"}, BackupScopeDigest: scope, ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, Verification: projection, SourceTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "source-token"}, Key: "token"}, RecoveryKeySecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "recovery-key"}, Key: "key"}, Approvals: []ha.MigrationApproval{{ID: "backup", Approved: true, Digest: report.ID, TargetUID: "target-qualification", ScopeDigest: scope, VerificationDigest: vd}, {ID: "backup-settings-stable", Approved: true, Digest: report.ID, TargetUID: "target-qualification", ScopeDigest: scope, VerificationDigest: vd}}}}
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
	// The qualifier uses direct API reads and is never registered in the
	// production manager. Its isolation callback checks the exact policy UID.
	options := controller.BackupQualificationOptions{Image: image, ServiceAccount: sa.Name, ArchiveSize: resource.MustParse("128Mi"), VerifyIsolation: func(ctx context.Context, current *ha.HomeAssistantMigration) error {
		p := &networkingv1.NetworkPolicy{}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(policy), p); err != nil || p.UID != policy.UID || current.UID != op.UID {
			return fmt.Errorf("isolation changed")
		}
		return nil
	}}
	options.ReadBackupResolution = func(ctx context.Context, token string, key []byte, request ha.MigrationExecutionRequest, e migrationprotocol.Evidence) (migration.BackupResolutionObservation, error) {
		return migration.ReadBackupResolution(ctx, api, token, key, request, e)
	}
	reconciler, err := controller.NewIsolatedBackupQualificationReconciler(kube, scheme, options)
	if err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(op)}
	// Accept the immutable request before granting access to its exact receipt.
	for i := 0; i < 2; i++ {
		if _, err := reconciler.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if err := kube.Get(ctx, req.NamespacedName, op); err != nil {
		t.Fatal(err)
	}
	if len(op.Spec.ExecutionRequests) != 1 {
		t.Fatal("backup request not accepted")
	}
	r := op.Spec.ExecutionRequests[0]
	_, desiredRole, _, err := controller.BuildBackupWorkerRBAC(scheme, op, r, sa.Name)
	if err != nil {
		t.Fatal(err)
	}
	role.Rules = desiredRole.Rules
	// The test creates the immutable resolution hint only after dispatch.
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
	for _, name := range []string{"source-token", "recovery-key", "unrelated-secret"} {
		sar := &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{User: "system:serviceaccount:" + ns + ":worker", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}, ResourceAttributes: &authv1.ResourceAttributes{Namespace: ns, Verb: "get", Resource: "secrets", Name: name}}}
		create(sar)
		if sar.Status.Allowed != (name != "unrelated-secret") {
			t.Fatal("worker secret access is not restricted:", name)
		}
	}
	for _, name := range []string{migration.WorkerReceiptName(r.RequestID), "descriptor", "unrelated-configmap"} {
		sar := &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{User: "system:serviceaccount:" + ns + ":worker", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}, ResourceAttributes: &authv1.ResourceAttributes{Namespace: ns, Verb: "update", Resource: "configmaps", Name: name}}}
		create(sar)
		if sar.Status.Allowed != (name == migration.WorkerReceiptName(r.RequestID)) {
			t.Fatal("worker configmap update access is not restricted:", name)
		}
	}
	lostResponse := os.Getenv("HA_MIGRATION_RESOLVE_LOST") == "true"
	injected := false
	waitReady := func() {
		backupEventually(t, ctx, func() bool {
			if lostResponse && !injected {
				injected = backupSimulateLostReceipt(t, ctx, kube, op, r)
			}
			if _, err := reconciler.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			current := &ha.HomeAssistantMigration{}
			if err := kube.Get(ctx, req.NamespacedName, current); err != nil {
				t.Fatal(err)
			}
			jobs := &batchv1.JobList{}
			if err := kube.List(ctx, jobs, client.InNamespace(ns)); err != nil {
				t.Fatal(err)
			}
			if current.Status.Phase == "Unknown" {
				for _, j := range jobs.Items {
					for _, c := range j.Status.Conditions {
						if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
							t.Fatal("worker failed before a resolvable receipt")
						}
					}
				}
			}
			if current.Status.Phase == "Blocked" {
				for _, c := range current.Status.Conditions {
					if c.Reason != "ExecutionAttemptNotStarted" {
						t.Fatal("backup qualification blocked:", c.Reason)
					}
				}
			}
			return current.Status.Phase == "BackupReady"
		}, "controller BackupReady")
	}
	waitReady()
	if lostResponse && !injected {
		t.Fatal("lost-receipt scenario was not injected")
	}
	if err := kube.Get(ctx, req.NamespacedName, op); err != nil {
		t.Fatal(err)
	}
	d, cm, err := migration.ReadValidatedBackupEvidence(ctx, kube, op, r)
	if err != nil {
		t.Fatal(err)
	}
	if d.Validation == nil || d.Validation.Profile != migration.ArchiveValidationProfile {
		t.Fatal("archive validation missing")
	}
	var counts map[string]int
	backupGetJSON(t, endpoint+"/counts", &counts)
	if counts["generate"] != 1 || counts["upload"] != 0 || counts["restore"] != 0 {
		t.Fatal("unexpected HA mutation counts", counts)
	}
	t.Logf("HA=2026.11.0 worker=%s request=%s jobID=%s PVC=%s digest=%s size=%d generate=%d", image, r.RequestID, d.JobID, d.PVCUID, d.Digest, d.Size, counts["generate"])
	// Stop only this disposable source, preserving the PVC and immutable final
	// evidence. Losing the descriptor forces a new read-only Job to reopen and
	// authenticate the file, rather than merely trusting historical status.
	if err := kube.Delete(ctx, sourcePod); err != nil {
		t.Fatal(err)
	}
	if !lostResponse {
		if err := kube.Delete(ctx, cm); err != nil {
			t.Fatal(err)
		}
	}
	op.Status = ha.HomeAssistantMigrationStatus{}
	if err := kube.Status().Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	reconciler, err = controller.NewIsolatedBackupQualificationReconciler(kube, scheme, options)
	if err != nil {
		t.Fatal(err)
	}
	waitReady()
	if err := kube.Get(ctx, req.NamespacedName, op); err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := migration.ReadValidatedBackupEvidence(ctx, kube, op, r)
	if err != nil || reloaded.Digest != d.Digest {
		t.Fatal("offline PVC revalidation failed", err)
	}
	jobs := &batchv1.JobList{}
	if err := kube.List(ctx, jobs, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	mutating, resolving := 0, 0
	for _, j := range jobs.Items {
		if strings.HasPrefix(j.Name, "migration-resolve-") {
			resolving++
			if !strings.Contains(strings.Join(j.Spec.Template.Spec.Containers[0].Args, " "), "--mode observe-backup") {
				t.Fatal("resolution job is not read-only")
			}
		}
		if !strings.HasPrefix(j.Name, "migration-observe-") && !strings.HasPrefix(j.Name, "migration-resolve-") {
			mutating++
		}
		if j.Spec.BackoffLimit == nil || *j.Spec.BackoffLimit != 0 || j.Spec.TTLSecondsAfterFinished != nil {
			t.Fatal("unsafe Job lifecycle")
		}
	}
	if mutating != 1 || len(op.Spec.ExecutionDispatches) != 1 {
		t.Fatal("dispatch attempt repeated")
	}
	if lostResponse {
		receipt := &corev1.ConfigMap{}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: migration.WorkerReceiptName(r.RequestID)}, receipt); err != nil {
			t.Fatal(err)
		}
		var dispatch migration.BackupDispatchReceipt
		if json.Unmarshal([]byte(receipt.Data["receipt.json"]), &dispatch) != nil || dispatch.Phase != "Dispatching" || dispatch.JobID != "" || resolving != 1 {
			t.Fatal("lost receipt changed or read-only resolution job missing")
		}
	}
	t.Logf("offline restart: BackupReady restored from PVC; jobs=%d dispatches=%d; no target/upload/restore", len(jobs.Items), len(op.Spec.ExecutionDispatches))
}

// Simulate the worker's accepted-receipt update being lost after HA accepted
// generate. The historical dispatch receipt remains; the test administrator
// supplies the externally observed ID in a separate immutable ConfigMap.
func backupSimulateLostReceipt(t *testing.T, ctx context.Context, kube client.Client, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest) bool {
	t.Helper()
	job := &batchv1.Job{}
	name := "migration-" + strings.TrimPrefix(r.RequestID, "sha256:")[:40]
	if kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: name}, job) != nil || job.Status.Succeeded == 0 {
		return false
	}
	cm := &corev1.ConfigMap{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: migration.WorkerReceiptName(r.RequestID)}, cm); err != nil {
		return false
	}
	var receipt migration.BackupDispatchReceipt
	if json.Unmarshal([]byte(cm.Data["receipt.json"]), &receipt) != nil || receipt.Phase != "Accepted" || receipt.JobID == "" {
		return false
	}
	e := migrationprotocol.Evidence{Version: "v1", OperationUID: string(op.UID), RequestID: r.RequestID, Stage: r.Stage, SourceID: r.SourceID, TargetUID: r.Target.UID, JobID: receipt.JobID, BackupID: receipt.JobID}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	hint := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "resolution-evidence", Namespace: op.Namespace}, Immutable: &yes, Data: map[string]string{"result.json": string(b)}}
	if err := kube.Create(ctx, hint); err != nil {
		t.Fatal(err)
	}
	receipt.Phase = "Dispatching"
	receipt.JobID = ""
	b, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["receipt.json"] = string(b)
	if err := kube.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	current := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Resolutions = append(current.Spec.Resolutions, ha.MigrationResolution{Version: "v1", Stage: r.Stage, RequestID: r.RequestID, Decision: "ObserveExternalResult", EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: hint.Name, UID: string(hint.UID), Key: "result.json"}})
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	return true
}

// This is fixture teardown, not the migration cancellation implementation. The
// namespace controller stops all local workloads first; only then may this test
// remove its own operation finalizer. It never contacts HA or a different UID.
func backupCleanupNamespace(t *testing.T, kube client.Client, created *corev1.Namespace) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	current := &corev1.Namespace{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(created), current); apierrors.IsNotFound(err) {
		return
	} else if err != nil {
		t.Error(err)
		return
	}
	if current.UID != created.UID {
		t.Error("test namespace UID changed; teardown refused")
		return
	}
	if err := kube.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		t.Error(err)
		return
	}
	for {
		pods := &corev1.PodList{}
		jobs := &batchv1.JobList{}
		if err := kube.List(ctx, pods, client.InNamespace(created.Name)); apierrors.IsNotFound(err) {
			return
		} else if err != nil {
			t.Error(err)
			return
		}
		if err := kube.List(ctx, jobs, client.InNamespace(created.Name)); apierrors.IsNotFound(err) {
			return
		} else if err != nil {
			t.Error(err)
			return
		}
		if len(pods.Items) == 0 && len(jobs.Items) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Error("fixture workloads still exist; operation finalizer retained")
			return
		case <-time.After(time.Second):
		}
	}
	op := &ha.HomeAssistantMigration{}
	key := client.ObjectKey{Namespace: created.Name, Name: "backup"}
	if err := kube.Get(ctx, key, op); apierrors.IsNotFound(err) {
		return
	} else if err != nil {
		t.Error(err)
		return
	}
	if op.DeletionTimestamp.IsZero() {
		if err := kube.Delete(ctx, op); err != nil {
			t.Error(err)
			return
		}
		if err := kube.Get(ctx, key, op); apierrors.IsNotFound(err) {
			return
		} else if err != nil {
			t.Error(err)
			return
		}
	}
	if controllerutil.RemoveFinalizer(op, "ha.homeassistant.io/migration-operation-finalizer") {
		if err := kube.Update(ctx, op); err != nil {
			t.Error(err)
		}
	}
}

func backupEventually(t *testing.T, ctx context.Context, check func() bool, description string) {
	t.Helper()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("timeout:", description)
		case <-time.After(time.Second):
		}
	}
}

func backupPortForward(t *testing.T, ctx context.Context, ns, pod string, remote int) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	child, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(child, "kubectl", "port-forward", "-n", ns, "pod/"+pod, fmt.Sprintf("%d:%d", port, remote), "--address=127.0.0.1")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), func() { cancel(); _ = cmd.Wait() }
}

func backupGetJSON(t *testing.T, url string, value any) {
	t.Helper()
	c := &http.Client{Timeout: 10 * time.Second}
	response, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if err := json.NewDecoder(response.Body).Decode(value); err != nil {
		t.Fatal(err)
	}
}

func backupPrepareVerification(t *testing.T, ctx context.Context, api *haclient.Client, endpoint, token string) []ha.MigrationVerificationItem {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	flow, err := api.StartConfigFlow(ctx, token, "local_todo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.SubmitConfigFlow(ctx, token, flow.FlowID, map[string]interface{}{"todo_list_name": "Backup Qualification"}); err != nil {
		t.Fatal(err)
	}
	var entryID, entityID string
	backupEventually(t, ctx, func() bool {
		entries, err := api.ListConfigEntries(ctx, token)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.Domain == "local_todo" && e.Title == "Backup Qualification" {
				entryID = e.EntryID
			}
		}
		raw, err := api.SendWebSocketCommand(ctx, token, "config/entity_registry/list", nil)
		if err != nil {
			return false
		}
		var items []struct {
			EntityID      string `json:"entity_id"`
			ConfigEntryID string `json:"config_entry_id"`
		}
		if json.Unmarshal(raw, &items) != nil {
			return false
		}
		for _, e := range items {
			if entryID != "" && e.ConfigEntryID == entryID {
				entityID = e.EntityID
			}
		}
		return entityID != ""
	}, "verification identities")
	if err := utils.AddLocalTodoItem(ctx, nil, endpoint, token, entityID, "backup qualification evidence"); err != nil {
		t.Fatal(err)
	}
	result := make([]ha.MigrationVerificationItem, 0, 3)
	for _, item := range []struct{ kind, id string }{{"EntityRegistryIdentity", entityID}, {"IntegrationEntryIdentity", entryID}, {"TodoItems", entityID}} {
		projection, err := api.ReadMigrationVerification(ctx, token, item.kind, item.id)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := migrationprotocol.ProjectionDigest(item.kind, item.id, projection)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, ha.MigrationVerificationItem{Kind: item.kind, ID: item.id, ExpectedDigest: digest})
	}
	return result
}

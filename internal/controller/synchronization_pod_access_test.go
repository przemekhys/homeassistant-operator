package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
)

func TestReconcileSynchronizationWriterAccess(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("writer-instance"),
	}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha).Build()
	r := &HomeAssistantReconciler{Client: kube, Scheme: scheme}
	if err := r.reconcileSynchronizationWriterAccess(ctx, ha); err != nil {
		t.Fatal(err)
	}

	name := synchronizationWriterServiceAccountName(ha)
	serviceAccount := &corev1.ServiceAccount{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: name}, serviceAccount); err != nil {
		t.Fatalf("writer ServiceAccount was not created: %v", err)
	}
	if len(serviceAccount.OwnerReferences) != 1 || serviceAccount.OwnerReferences[0].UID != ha.UID {
		t.Fatalf("writer ServiceAccount must be controlled by HomeAssistant: %#v", serviceAccount.OwnerReferences)
	}

	role := &rbacv1.Role{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: name}, role); err != nil {
		t.Fatalf("writer Role was not created: %v", err)
	}
	if len(role.Rules) != 1 || len(role.Rules[0].Resources) != 1 || role.Rules[0].Resources[0] != "configmaps" ||
		len(role.Rules[0].Verbs) != 2 || role.Rules[0].Verbs[0] != "get" || role.Rules[0].Verbs[1] != "update" ||
		len(role.Rules[0].ResourceNames) != 1 || role.Rules[0].ResourceNames[0] != coordination.RootName(ha.UID) {
		t.Fatalf("writer Role must permit only get/update of its coordinator: %#v", role.Rules)
	}

	binding := &rbacv1.RoleBinding{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: name}, binding); err != nil {
		t.Fatalf("writer RoleBinding was not created: %v", err)
	}
	if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != name || len(binding.Subjects) != 1 ||
		binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != name {
		t.Fatalf("writer RoleBinding must grant only the per-instance Role to its ServiceAccount: %#v", binding)
	}
}

func TestSynchronizationWriterAccessIsDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("writer-instance"),
	}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha).Build()
	r := &HomeAssistantReconciler{Client: kube, Scheme: scheme}
	if err := r.reconcileSynchronizationWriterAccess(ctx, ha); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: synchronizationWriterServiceAccountName(ha)}, &corev1.ServiceAccount{}); err == nil {
		t.Fatal("default-disabled coordination must not create a writer ServiceAccount")
	}
}

func TestSynchronizationWriterAccessIsRetainedDuringRestoreHold(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	now := metav1.Now()
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("writer-instance"),
		Finalizers: []string{synchronizationProtectionFinalizer}, DeletionTimestamp: &now,
	}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha).Build()
	store := coordination.Store{Reader: kube, Client: kube}
	if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
		root.RestoreHold = &coordination.Hold{OperationUID: "restore", Reason: "Restoring"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := &HomeAssistantReconciler{
		Client: kube, Scheme: scheme, Coordination: CoordinationDependencies{APIReader: kube, Store: store},
	}
	if err := r.reconcileSynchronizationWriterAccess(ctx, ha); err != nil {
		t.Fatal(err)
	}
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ha)})
	if err != nil || result.RequeueAfter <= 0 {
		t.Fatalf("restore hold must retain runtime writer identity and requeue: result=%#v err=%v", result, err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: synchronizationWriterServiceAccountName(ha)}, &corev1.ServiceAccount{}); err != nil {
		t.Fatalf("writer ServiceAccount was removed during restore hold: %v", err)
	}
}

func TestSynchronizationWriterTokenExcludesHomeAssistantContainer(t *testing.T) {
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home", UID: types.UID("writer-instance")}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	sts := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "config-init"}},
		Containers:     []corev1.Container{{Name: "home-assistant"}, {Name: "community-repository"}},
	}}}}
	configureSynchronizationWriterPodAccess(sts, ha)
	if sts.Spec.Template.Spec.ServiceAccountName != synchronizationWriterServiceAccountName(ha) {
		t.Fatal("coordinated pod must use its per-instance writer ServiceAccount")
	}
	if sts.Spec.Template.Spec.AutomountServiceAccountToken == nil || *sts.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("pod must disable implicit ServiceAccount token mounting")
	}
	if len(sts.Spec.Template.Spec.Volumes) != 1 || sts.Spec.Template.Spec.Volumes[0].Projected == nil {
		t.Fatal("writer token must use an explicit projected volume")
	}
	projections := sts.Spec.Template.Spec.Volumes[0].Projected.Sources
	if len(projections) != 2 || projections[0].ServiceAccountToken == nil || projections[1].ConfigMap == nil ||
		projections[1].ConfigMap.Name != "kube-root-ca.crt" {
		t.Fatalf("writer identity must project only a token and the Kubernetes CA: %#v", projections)
	}
	if !hasSynchronizationWriterTokenMount(sts.Spec.Template.Spec.InitContainers[0]) ||
		!hasSynchronizationWriterTokenMount(sts.Spec.Template.Spec.Containers[1]) {
		t.Fatal("only pod-side writers must receive the projected token")
	}
	if hasSynchronizationWriterTokenMount(sts.Spec.Template.Spec.Containers[0]) {
		t.Fatal("main Home Assistant container must not receive the projected token")
	}
	for _, container := range []corev1.Container{sts.Spec.Template.Spec.InitContainers[0], sts.Spec.Template.Spec.Containers[1]} {
		if !hasWriterCoordinationEnvironment(container) {
			t.Fatalf("writer %q must receive only its coordination identity", container.Name)
		}
	}
}

func TestWriterServiceAccountChangeTriggersControlledRollout(t *testing.T) {
	current := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "home-assistant"}},
	}}}}
	desired := current.DeepCopy()
	desired.Spec.Template.Spec.ServiceAccountName = "home-sync-writer"
	if !needsUpdate(current, desired) {
		t.Fatal("a coordinated-writer ServiceAccount upgrade must trigger a controlled rollout")
	}
}

func TestPodWritersFailClosedWithoutCoordinationAPI(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is unavailable")
	}
	configDir := t.TempDir()
	command := exec.Command("python3", "-c", configurationInitScript)
	command.Env = append(os.Environ(),
		"HA_SYNC_COORDINATION_CONFIGMAP=instance-ha-coordination",
		"POD_NAMESPACE=default",
		"HA_SYNC_WRITER=pod/test/config-init",
		"HA_CONFIG_DIR="+configDir,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("writer must refuse to write when its coordination API credentials are unavailable")
	}
	if strings.Contains(string(output), "synthetic") {
		t.Fatal("coordination failure must not disclose request data")
	}
	for _, name := range []string{"automations.yaml", "scenes.yaml", "scripts.yaml"} {
		if _, statErr := os.Stat(filepath.Join(configDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("writer created %s after coordination failure", name)
		}
	}
}

func hasSynchronizationWriterTokenMount(container corev1.Container) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == synchronizationWriterTokenName && mount.MountPath == synchronizationWriterTokenPath && mount.ReadOnly {
			return true
		}
	}
	return false
}

func hasWriterCoordinationEnvironment(container corev1.Container) bool {
	seen := map[string]bool{}
	for _, env := range container.Env {
		seen[env.Name] = true
	}
	return seen["POD_NAMESPACE"] && seen["POD_UID"] && seen["HA_SYNC_COORDINATION_CONFIGMAP"] && seen["HA_SYNC_WRITER"]
}

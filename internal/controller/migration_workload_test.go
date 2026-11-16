package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func TestMigrationRestoreWorkloadRequiresMatchingSourceStopAndRestoreApprovals(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
		Spec: hav1.HomeAssistantSpec{
			Alpha: &hav1.AlphaSpec{
				MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true},
			},
		},
	}
	migration := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default"},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			Target: hav1alpha1.MigrationTarget{Name: "target", UID: "target-uid"},
			Archive: &hav1alpha1.MigrationArchive{
				BackupID: "backup",
				AgentID:  "backup.local",
				Digest:   "archive",
			},
			RestoreScopeDigest: "scope",
			Approvals: []hav1alpha1.MigrationApproval{
				{
					ID:            "restore",
					Digest:        "decision",
					Approved:      true,
					TargetUID:     "target-uid",
					ArchiveDigest: "archive",
					ScopeDigest:   "scope",
				},
			},
		},
	}
	reconciler := &HomeAssistantReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(migration).Build(),
	}
	allowed, err := reconciler.migrationRestoreStartAllowed(context.Background(), ha)
	if err != nil || allowed {
		t.Fatalf("start before source-stop = %t, %v", allowed, err)
	}
	migration.Spec.Approvals = append(
		migration.Spec.Approvals,
		hav1alpha1.MigrationApproval{ID: "source-stop", Digest: "confirmed", Approved: true},
	)
	if err := reconciler.Update(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	allowed, err = reconciler.migrationRestoreStartAllowed(context.Background(), ha)
	if err != nil || allowed {
		t.Fatalf("unprotected onboarding started after approvals = %t, %v", allowed, err)
	}
	migration.Spec.Archive.Digest = "changed-archive"
	if err := reconciler.Update(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	allowed, err = reconciler.migrationRestoreStartAllowed(context.Background(), ha)
	if err != nil || allowed {
		t.Fatalf("start after archive changed = %t, %v", allowed, err)
	}
}

func TestMigrationRestorePreparationBlocksBootstrapBeforeAnyHARequest(t *testing.T) {
	ha := &hav1.HomeAssistant{Spec: hav1.HomeAssistantSpec{
		Alpha: &hav1.AlphaSpec{
			MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true},
		},
		Bootstrap: &hav1.BootstrapSpec{Enabled: true},
	}}
	reconciler := &HomeAssistantReconciler{}
	result, err := reconciler.reconcileBootstrap(context.Background(), ha)
	if err != nil || result.RequeueAfter != 0 || ha.Status.Bootstrap != nil {
		t.Fatalf("bootstrap during restore preparation = %#v, %v", result, err)
	}
	initContainers, err := reconciler.buildInitContainers(context.Background(), ha)
	if err != nil || len(initContainers) != 0 {
		t.Fatalf("init containers during restore preparation = %#v, %v", initContainers, err)
	}
}

func TestMigrationRestoreStatefulSetOmitsConfigurationProjections(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
		Spec: hav1.HomeAssistantSpec{
			Alpha: &hav1.AlphaSpec{
				MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true},
			},
			AdditionalVolumes: &hav1.AdditionalVolumesSpec{
				Volumes: []corev1.Volume{{Name: "unsafe-config"}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "unsafe-config", MountPath: "/config/unsafe"},
				},
			},
		},
	}
	reconciler := &HomeAssistantReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}
	sts, err := reconciler.buildStatefulSet(context.Background(), ha)
	if err != nil {
		t.Fatal(err)
	}
	if *sts.Spec.Replicas != 0 || len(sts.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatalf("restore StatefulSet started or ran init: %#v", sts.Spec)
	}
	selector := sts.Spec.Template.Labels[migrationTargetLabel]
	if selector == "" || selector != migrationTargetSelector(string(ha.UID)) || selector == migrationTargetSelector("different-uid") {
		t.Fatal("restore target selector is not bound to target UID")
	}
	if _, immutableSelector := sts.Spec.Selector.MatchLabels[migrationTargetLabel]; immutableSelector {
		t.Fatal("migration label must not change the StatefulSet selector")
	}
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name != "config" && volume.Name != "device-0" {
			t.Fatalf("restore StatefulSet projected volume %q", volume.Name)
		}
	}
}

func TestMigrationRestoreDoesNotWriteConfigurationOrRouteTraffic(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
		Spec: hav1.HomeAssistantSpec{
			Alpha: &hav1.AlphaSpec{
				MigrationRestore: &hav1.MigrationRestoreAlphaSpec{Enabled: true},
			},
		},
	}
	configuration := &hav1.HomeAssistantConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "target-config", Namespace: "default"},
		Spec: hav1.HomeAssistantConfigurationSpec{
			HomeAssistantRef: hav1.HomeAssistantReference{Name: "target"},
			Configuration:    "homeassistant:\n  name: must-not-project\n",
		},
	}
	sourceService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "default"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"instance": "source"}},
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ha, configuration, sourceService).
		Build()
	configReconciler := &HomeAssistantConfigurationReconciler{Client: kube, Scheme: scheme}
	if _, err := configReconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "default", Name: "target-config"}}); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "target-configuration"}, configMap); err == nil {
		t.Fatal("restore preparation projected configuration ConfigMap")
	}
	workloadReconciler := &HomeAssistantReconciler{Client: kube, Scheme: scheme}
	if _, err := workloadReconciler.migrationRestoreStartAllowed(context.Background(), ha); err != nil {
		t.Fatal(err)
	}
	currentService := &corev1.Service{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "source"}, currentService); err != nil {
		t.Fatal(err)
	}
	if currentService.Spec.Selector["instance"] != "source" {
		t.Fatal("restore gate modified source routing")
	}
}

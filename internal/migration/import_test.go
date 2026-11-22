package migration

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func TestImportFloorIsPausedOwnedCreateOnlyAndRetryable(t *testing.T) {
	report := &Report{
		SourceID: "source",
		Target:   Target{Namespace: "default", Name: "target", UID: "target-uid"},
		Objects: []ReportObject{
			{
				Kind:           "Floor",
				SourceID:       "floor-id",
				Name:           floorName("floor-id"),
				Classification: ClassificationImportable,
				Proposal:       json.RawMessage(`{"name":"Ground","icon":"","level":0}`),
			},
		},
	}
	if err := report.Finalize(); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
	}
	migration := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration",
			Namespace: "default",
			UID:       "migration-uid",
		},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: report.SourceID,
			Target:   hav1alpha1.MigrationTarget{Name: "target", UID: "target-uid"},
			ReportID: report.ID,
		},
	}
	scheme := importTestScheme(t)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(target, migration).Build()
	request := ImportRequest{
		Report:    report,
		ReportID:  report.ID,
		Namespace: "default",
		Name:      "migration",
		Confirmed: true,
		Scheme:    scheme,
	}
	if _, err := Import(context.Background(), kube, request); err != nil {
		t.Fatal(err)
	}
	floor := &hav1.HomeAssistantFloor{}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: floorName("floor-id")}, floor); err != nil {
		t.Fatal(err)
	}
	owner := metav1.GetControllerOf(floor)
	if owner == nil || owner.UID != "migration-uid" ||
		floor.Annotations["ha.homeassistant.io/migration-paused"] != "true" ||
		floor.Annotations["ha.homeassistant.io/migration-source-id"] != "floor-id" ||
		floor.Spec.Name != "Ground" ||
		floor.Spec.Level == nil ||
		*floor.Spec.Level != 0 {
		t.Fatalf("floor=%#v", floor)
	}
	if _, err := Import(context.Background(), kube, request); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	floor.Spec.Name = "user change"
	if err := kube.Update(context.Background(), floor); err != nil {
		t.Fatal(err)
	}
	result, err := Import(context.Background(), kube, request)
	if err == nil || result == nil || !result.Partial {
		t.Fatalf("collision was not reported: %#v %v", result, err)
	}
}

func importTestReport(t *testing.T) *Report {
	t.Helper()
	report := &Report{
		SourceID: "source",
		Target:   Target{Namespace: "default", Name: "target", UID: "target-uid"},
		Objects: []ReportObject{
			{
				Kind:           "Integration",
				SourceID:       "entry",
				Name:           "todo",
				Classification: ClassificationUnsupported,
				Reason:         "unverified",
			},
		},
	}
	if err := report.Finalize(); err != nil {
		t.Fatal(err)
	}
	return report
}

func importTestClient(t *testing.T, objects ...runtime.Object) *fake.ClientBuilder {
	t.Helper()
	scheme := importTestScheme(t)
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...)
}

func importTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
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
	return scheme
}

func TestImportCreatesOnlyMigrationAndSafelyReusesExactRetry(t *testing.T) {
	report := importTestReport(t)
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
	}
	kube := importTestClient(t, target).Build()
	request := ImportRequest{
		Report:    report,
		ReportID:  report.ID,
		Namespace: "default",
		Name:      "migration",
		Confirmed: true,
	}
	result, err := Import(context.Background(), kube, request)
	if err != nil || result.Reused || result.Migration.Spec.ReportID != report.ID || result.Migration.Spec.ExecutionProtocol != "v1" {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	retry, err := Import(context.Background(), kube, request)
	if err != nil || !retry.Reused || retry.Migration.Spec.ExecutionProtocol != "v1" {
		t.Fatalf("safe retry = %#v, %v", retry, err)
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "migration"}, stored); err != nil {
		t.Fatal(err)
	}
}

func TestImportBindsSourceEndpointToApprovedReport(t *testing.T) {
	report := importTestReport(t)
	report.SourceID = "https://source.example.test"
	if err := report.Finalize(); err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"}}
	kube := importTestClient(t, target).Build()
	result, err := Import(context.Background(), kube, ImportRequest{Report: report, ReportID: report.ID, Namespace: "default", Name: "migration", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Migration.Spec.SourceEndpoint != report.SourceID {
		t.Fatal("endpoint is not bound to approved source")
	}
}

func TestImportRejectsUnconfirmedReportTargetAndNameCollisions(t *testing.T) {
	report := importTestReport(t)
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
	}
	existing := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "default"},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: "other",
			Target:   hav1alpha1.MigrationTarget{Name: "target", UID: "target-uid"},
			ReportID: "other",
		},
	}
	kube := importTestClient(t, target, existing).Build()
	request := ImportRequest{
		Report:    report,
		ReportID:  report.ID,
		Namespace: "default",
		Name:      "migration",
	}
	if _, err := Import(context.Background(), kube, request); err == nil {
		t.Fatal("unconfirmed import succeeded")
	}
	request.Confirmed = true
	if _, err := Import(context.Background(), kube, request); err == nil {
		t.Fatal("colliding migration succeeded")
	}
	report.Target.UID = "wrong"
	if err := report.Finalize(); err != nil {
		t.Fatal(err)
	}
	request.ReportID = report.ID
	request.Name = "other-migration"
	if _, err := Import(context.Background(), kube, request); err == nil {
		t.Fatal("wrong target UID succeeded")
	}
}

func TestImportRejectsReportForDifferentNamespaceBeforeCreatingResources(t *testing.T) {
	report := importTestReport(t)
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "other", UID: "target-uid"},
	}
	kube := importTestClient(t, target).Build()
	_, err := Import(
		context.Background(),
		kube,
		ImportRequest{
			Report:    report,
			ReportID:  report.ID,
			Namespace: "other",
			Name:      "migration",
			Confirmed: true,
		},
	)
	if err == nil {
		t.Fatal("accepted report for a different namespace")
	}
	stored := &hav1alpha1.HomeAssistantMigration{}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: "other", Name: "migration"}, stored); err == nil {
		t.Fatal("created migration for wrong namespace")
	}
}

func TestImportPersistsSourceTokenReferenceAndRejectsChangedRetry(t *testing.T) {
	report := importTestReport(t)
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"}}
	kube := importTestClient(t, target).Build()
	ref := &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "source-token"}, Key: "token"}
	recovery := &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "recovery"}, Key: "key"}
	request := ImportRequest{Report: report, ReportID: report.ID, Namespace: "default", Name: "migration", Confirmed: true, SourceTokenSecretRef: ref, RecoveryKeySecretRef: recovery}
	result, err := Import(context.Background(), kube, request)
	if err != nil || result.Migration.Spec.SourceTokenSecretRef == nil || result.Migration.Spec.SourceTokenSecretRef.Name != "source-token" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := Import(context.Background(), kube, request); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.Migration.Spec.RecoveryKeySecretRef == nil || result.Migration.Spec.RecoveryKeySecretRef.Name != "recovery" {
		t.Fatal("recovery reference was not stored")
	}
	request.RecoveryKeySecretRef = &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "other"}, Key: "key"}
	if _, err := Import(context.Background(), kube, request); err == nil {
		t.Fatal("changed recovery reference accepted")
	}
	request.RecoveryKeySecretRef = recovery
	request.SourceTokenSecretRef = &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "other"}, Key: "token"}
	if _, err := Import(context.Background(), kube, request); err == nil {
		t.Fatal("changed source credential reference was accepted on retry")
	}
}

func TestImportPersistsApprovedBackupScopeDigest(t *testing.T) {
	report := importTestReport(t)
	include := true
	report.Backup = &BackupPreflight{Status: "Ready", Protected: true, IncludeDatabase: &include, IncludeAllAddons: &include, IncludeAddons: json.RawMessage(`null`), IncludeFolders: json.RawMessage(`null`),
		AgentIDs: []string{"backup.local"}}
	if err := report.Finalize(); err != nil {
		t.Fatal(err)
	}
	want, err := BackupScopeDigest(report.Backup)
	if err != nil {
		t.Fatal(err)
	}
	target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"}}
	kube := importTestClient(t, target).Build()
	result, err := Import(context.Background(), kube, ImportRequest{
		Report: report, ReportID: report.ID, Namespace: "default", Name: "migration", Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Migration.Spec.BackupScopeDigest != want {
		t.Fatal("approved scope digest was not persisted")
	}
}

func TestConvertReportFailsClosedForUnverifiedImportableKind(t *testing.T) {
	report := importTestReport(t)
	report.Objects[0].Classification = ClassificationImportable
	if err := ConvertReport(report); err == nil {
		t.Fatal("unverified conversion succeeded")
	}
}

func TestImportResumesPartialSecretCreationWithoutUpdatingMigration(t *testing.T) {
	report := importTestReport(t)
	target := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default", UID: "target-uid"},
	}
	existing := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "migration",
			Namespace: "default",
			UID:       "migration-uid",
		},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			SourceID: report.SourceID,
			Target: hav1alpha1.MigrationTarget{
				Name: report.Target.Name,
				UID:  report.Target.UID,
			},
			ReportID: report.ID,
		},
	}
	scheme := importTestScheme(t)
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(target, existing).
		Build()
	result, err := Import(
		context.Background(),
		kube,
		ImportRequest{
			Report:     report,
			ReportID:   report.ID,
			Namespace:  "default",
			Name:       "migration",
			Confirmed:  true,
			SecretData: map[string][]byte{"available": []byte("private")},
			Scheme:     scheme,
		},
	)
	if err != nil || !result.Reused || result.Secret == nil ||
		len(result.Secret.OwnerReferences) != 1 ||
		result.Secret.OwnerReferences[0].UID != existing.UID {
		t.Fatalf("partial import retry = %#v, %v", result, err)
	}
}

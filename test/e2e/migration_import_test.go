package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/gomega"
	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/test/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

func runMigrationImportScenario(namespace, haName string) {
	api, cleanup := criticalPathHAClient(namespace, haName)
	defer cleanup()
	token := criticalPathOperatorToken(namespace, haName+"-homeassistant-api-token")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	name := "import-e2e-" + utils.RandomString(8)
	raw, err := api.SendWebSocketCommand(
		ctx,
		token,
		"config/floor_registry/create",
		map[string]interface{}{"name": name},
	)
	Expect(err).NotTo(HaveOccurred())
	var fixture struct {
		ID string `json:"floor_id"`
	}
	Expect(json.Unmarshal(raw, &fixture)).To(Succeed())
	Expect(fixture.ID).NotTo(BeEmpty())
	defer func() {
		_, _ = api.SendWebSocketCommand(
			context.Background(),
			token,
			"config/floor_registry/delete",
			map[string]interface{}{"floor_id": fixture.ID},
		)
	}()
	scheme := runtime.NewScheme()
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	Expect(hav1.AddToScheme(scheme)).To(Succeed())
	Expect(hav1alpha1.AddToScheme(scheme)).To(Succeed())
	config, err := ctrlconfig.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	kube, err := client.New(config, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	target := &hav1.HomeAssistant{}
	Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: haName}, target)).To(Succeed())
	report, err := migration.Scan(
		ctx,
		api,
		migration.ScanRequest{
			SourceID: "disposable-e2e",
			Token:    token,
			Target:   migration.Target{Namespace: namespace, Name: haName, UID: string(target.UID)},
		},
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(report.Backup).NotTo(BeNil(), "scan must report backup preflight")
	Expect(report.Backup.Status).To(BeElementOf("Ready", "Blocked"))
	encoded, err := json.Marshal(report)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(encoded)).NotTo(ContainSubstring(token))
	var floorName string
	for _, object := range report.Objects {
		if object.Kind == "Floor" && object.SourceID == fixture.ID &&
			object.Classification == migration.ClassificationImportable {
			floorName = object.Name
		}
	}
	Expect(floorName).NotTo(BeEmpty(), "complete floor should be importable")
	migrationName := "migration-import-" + utils.RandomString(8)
	secretValue := "private-fixture-" + utils.RandomString(16)
	tokenRef := &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: haName + "-homeassistant-api-token"}, Key: "token"}
	// Only the reference is tested here; this fixture is not an archive key.
	recoveryRef := &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "recovery-reference-fixture"}, Key: "key"}
	result, err := migration.Import(ctx, kube, migration.ImportRequest{
		Report: report, ReportID: report.ID, Namespace: namespace, Name: migrationName,
		Confirmed: true, Scheme: scheme, SourceTokenSecretRef: tokenRef, RecoveryKeySecretRef: recoveryRef,
		SecretData: map[string][]byte{"credential": []byte(secretValue)},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.Secret).NotTo(BeNil())
	secretOwner := metav1.GetControllerOf(result.Secret)
	Expect(secretOwner).NotTo(BeNil())
	Expect(secretOwner.UID).To(Equal(result.Migration.UID))
	storedMigration := &hav1alpha1.HomeAssistantMigration{}
	Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: migrationName}, storedMigration)).To(Succeed())
	Expect(storedMigration.Spec.SourceTokenSecretRef).NotTo(BeNil())
	Expect(storedMigration.Spec.SourceTokenSecretRef.Name).To(Equal(tokenRef.Name))
	Expect(storedMigration.Spec.SourceTokenSecretRef.Key).To(Equal(tokenRef.Key))
	Expect(storedMigration.Spec.RecoveryKeySecretRef).NotTo(BeNil())
	Expect(storedMigration.Spec.RecoveryKeySecretRef.Name).To(Equal(recoveryRef.Name))
	Expect(storedMigration.Spec.RecoveryKeySecretRef.Key).To(Equal(recoveryRef.Key))
	Expect(string(encoded)).NotTo(ContainSubstring(secretValue))
	defer func() { _ = kube.Delete(context.Background(), result.Migration) }()
	floor := &hav1.HomeAssistantFloor{}
	Eventually(func(g Gomega) {
		g.Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, floor)).To(Succeed())
		condition := meta.FindStatusCondition(floor.Status.Conditions, "MigrationAdoption")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Reason).To(Equal("MigrationPaused"))
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())
	owner := metav1.GetControllerOf(floor)
	Expect(owner).NotTo(BeNil())
	Expect(owner.UID).To(Equal(result.Migration.UID))
	Expect(floor.Annotations["ha.homeassistant.io/migration-paused"]).To(Equal("true"))
	encoded, err = json.Marshal(floor)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(encoded)).NotTo(ContainSubstring(token))
	Expect(string(encoded)).NotTo(ContainSubstring(secretValue))
	// Owner identity is a second durable guard if import annotations are
	// accidentally removed; it must not allow ordinary reconciliation.
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, floor); err != nil {
			return err
		}
		delete(floor.Annotations, "ha.homeassistant.io/migration-imported")
		delete(floor.Annotations, "ha.homeassistant.io/migration-paused")
		return kube.Update(ctx, floor)
	})).To(Succeed())
	Eventually(func(g Gomega) {
		g.Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, floor)).To(Succeed())
		condition := meta.FindStatusCondition(floor.Status.Conditions, "MigrationAdoption")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		g.Expect(condition.Reason).To(Equal("IncompleteIdentity"))
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, floor); err != nil {
			return err
		}
		floor.Annotations["ha.homeassistant.io/migration-imported"] = "true"
		floor.Annotations["ha.homeassistant.io/migration-paused"] = "true"
		return kube.Update(ctx, floor)
	})).To(Succeed())
	Expect(kube.Delete(ctx, floor)).To(Succeed())
	Eventually(func() bool {
		return apierrors.IsNotFound(
			kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, &hav1.HomeAssistantFloor{}),
		)
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(BeTrue())
	Eventually(func(g Gomega) {
		value, readErr := api.SendWebSocketCommand(ctx, token, "config/floor_registry/list", nil)
		g.Expect(readErr).NotTo(HaveOccurred())
		var floors []struct {
			ID   string `json:"floor_id"`
			Name string `json:"name"`
		}
		g.Expect(json.Unmarshal(value, &floors)).To(Succeed())
		count := 0
		for _, floor := range floors {
			if floor.ID == fixture.ID && floor.Name == name {
				count++
			}
		}
		g.Expect(count).To(Equal(1))
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())
	// Repeat the approved create-only import, then exercise the integrated
	// unpause/adoption path against the same durable HA identity.
	retried, err := migration.Import(
		ctx,
		kube,
		migration.ImportRequest{
			Report:               report,
			ReportID:             report.ID,
			Namespace:            namespace,
			Name:                 migrationName,
			Confirmed:            true,
			Scheme:               scheme,
			SourceTokenSecretRef: tokenRef,
			RecoveryKeySecretRef: recoveryRef,
		},
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(retried.Reused).To(BeTrue())
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, floor); err != nil {
			return err
		}
		delete(floor.Annotations, "ha.homeassistant.io/migration-paused")
		return kube.Update(ctx, floor)
	})).To(Succeed())
	Eventually(func(g Gomega) {
		g.Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, floor)).To(Succeed())
		condition := meta.FindStatusCondition(floor.Status.Conditions, "MigrationAdoption")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		g.Expect(condition.Reason).To(Equal("Adopted"))
		g.Expect(floor.Status.FloorID).To(Equal(fixture.ID))
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())
	Expect(kube.Delete(ctx, result.Migration)).To(Succeed())
	Eventually(func() bool {
		return apierrors.IsNotFound(
			kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: floorName}, &hav1.HomeAssistantFloor{}),
		)
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(BeTrue())
	Eventually(func() bool {
		return apierrors.IsNotFound(
			kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: result.Secret.Name}, &corev1.Secret{}),
		)
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(BeTrue())
	value, err := api.SendWebSocketCommand(ctx, token, "config/floor_registry/list", nil)
	Expect(err).NotTo(HaveOccurred())
	var remaining []struct {
		ID   string `json:"floor_id"`
		Name string `json:"name"`
	}
	Expect(json.Unmarshal(value, &remaining)).To(Succeed())
	count := 0
	for _, object := range remaining {
		if object.ID == fixture.ID && object.Name == name {
			count++
		}
	}
	Expect(count).To(Equal(1), "migration garbage collection must not delete the HA object")

	// A distinct restore target stays stopped while the migration owns its PVC.
	restoreName := "restore-gc-" + utils.RandomString(8)
	manifest := fmt.Sprintf(`apiVersion: ha.homeassistant.io/v1
kind: HomeAssistant
metadata:
  name: %s
  namespace: %s
spec:
  version: "2026.11.0"
  storage:
    size: "1Gi"
  alpha:
    migrationRestore:
      enabled: true
`, restoreName, namespace)
	Expect(utils.ApplyYAML(manifest, namespace)).To(Succeed())
	restoreTarget := &hav1.HomeAssistant{}
	Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: restoreName}, restoreTarget)).To(Succeed())
	operation := &hav1alpha1.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{Name: restoreName, Namespace: namespace},
		Spec: hav1alpha1.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1",
			SourceID:          "http://source.example",
			SourceEndpoint:    "http://source.example",
			Target:            hav1alpha1.MigrationTarget{Name: restoreName, UID: string(restoreTarget.UID)},
		},
	}
	Expect(kube.Create(ctx, operation)).To(Succeed())
	defer func() { _ = kube.Delete(context.Background(), operation) }()
	// The HomeAssistant controller creates the restore PVC only after its
	// required configuration CR exists. Keep this fixture aligned with the
	// restore flow: migration preparation must not bypass that prerequisite.
	restoreConfig := &hav1.HomeAssistantConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: restoreName + "-config", Namespace: namespace},
		Spec: hav1.HomeAssistantConfigurationSpec{
			HomeAssistantRef: hav1.HomeAssistantReference{Name: restoreName},
			Configuration:    "homeassistant:\n  name: Restore GC target\ndefault_config:\n",
		},
	}
	Expect(kube.Create(ctx, restoreConfig)).To(Succeed())
	pvc := &corev1.PersistentVolumeClaim{}
	Eventually(func(g Gomega) {
		g.Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: restoreName + "-data"}, pvc)).To(Succeed())
		pvcOwner := metav1.GetControllerOf(pvc)
		g.Expect(pvcOwner).NotTo(BeNil())
		g.Expect(pvcOwner.UID).To(Equal(operation.UID))
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(Succeed())
	Expect(kube.Delete(ctx, operation)).To(Succeed())
	Eventually(func() bool {
		return apierrors.IsNotFound(
			kube.Get(
				ctx,
				client.ObjectKey{Namespace: namespace, Name: restoreName + "-data"},
				&corev1.PersistentVolumeClaim{},
			),
		)
	}, utils.StatusUpdateTimeout, reconcileInterval).Should(BeTrue())
	Expect(kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: restoreName}, restoreTarget)).To(Succeed())
}

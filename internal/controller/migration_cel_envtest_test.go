package controller

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Migration CEL admission", func() {
	It("applies Kustomize isolation filters when the webhook is unavailable", func() {
		kustomization, err := os.ReadFile(filepath.Join("..", "..", "config", "default", "kustomization.yaml"))
		Expect(err).NotTo(HaveOccurred())
		var wiring struct {
			Patches []struct {
				Path   string `json:"path"`
				Target struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"target"`
			} `json:"patches"`
		}
		Expect(yaml.Unmarshal(kustomization, &wiring)).To(Succeed())
		foundPatch := false
		for _, patch := range wiring.Patches {
			if patch.Path == "migration-isolation-patch.yaml" {
				foundPatch = true
				Expect(patch.Target.Kind).To(Equal("ValidatingWebhookConfiguration"))
				Expect(patch.Target.Name).To(Equal("validating-webhook-configuration"))
			}
		}
		Expect(foundPatch).To(BeTrue())
		manager, err := os.ReadFile(filepath.Join("..", "..", "config", "manager", "manager.yaml"))
		Expect(err).NotTo(HaveOccurred())
		managerDocs := kyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manager), 4096)
		var deployment appsv1.Deployment
		for {
			var document map[string]interface{}
			err = managerDocs.Decode(&document)
			if err == io.EOF {
				break
			}
			Expect(err).NotTo(HaveOccurred())
			if document["kind"] == "Deployment" {
				data, marshalErr := yaml.Marshal(document)
				Expect(marshalErr).NotTo(HaveOccurred())
				Expect(yaml.Unmarshal(data, &deployment)).To(Succeed())
				break
			}
		}
		var operatorServiceAccount *corev1.EnvVar
		for i := range deployment.Spec.Template.Spec.Containers[0].Env {
			entry := &deployment.Spec.Template.Spec.Containers[0].Env[i]
			if entry.Name == "OPERATOR_SERVICE_ACCOUNT" {
				operatorServiceAccount = entry
			}
		}
		Expect(operatorServiceAccount).NotTo(BeNil())
		Expect(operatorServiceAccount.ValueFrom).NotTo(BeNil())
		Expect(operatorServiceAccount.ValueFrom.FieldRef).NotTo(BeNil())
		Expect(operatorServiceAccount.ValueFrom.FieldRef.FieldPath).To(Equal("spec.serviceAccountName"))
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "default", "migration-isolation-patch.yaml"))
		Expect(err).NotTo(HaveOccurred())
		var patches []struct {
			Operation string                        `json:"op"`
			Path      string                        `json:"path"`
			Value     admissionv1.ValidatingWebhook `json:"value"`
		}
		Expect(yaml.Unmarshal(data, &patches)).To(Succeed())
		Expect(patches).To(HaveLen(3))
		config := &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "migration-kustomize-isolation-test"}}
		endpoint, timeout := "https://127.0.0.1:1/validate", int32(1)
		for _, patch := range patches {
			Expect(patch.Operation).To(Equal("add"))
			Expect(patch.Path).To(Equal("/webhooks/-"))
			Expect(patch.Value.FailurePolicy).NotTo(BeNil())
			Expect(*patch.Value.FailurePolicy).To(Equal(admissionv1.Fail))
			Expect(patch.Value.ClientConfig.Service).NotTo(BeNil())
			Expect(patch.Value.ClientConfig.Service.Path).NotTo(BeNil())
			Expect(*patch.Value.ClientConfig.Service.Path).To(Equal("/validate-migration-isolation"))
			patch.Value.ClientConfig.Service = nil
			patch.Value.ClientConfig.URL = &endpoint
			patch.Value.TimeoutSeconds = &timeout
			config.Webhooks = append(config.Webhooks, patch.Value)
		}
		existingSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "migration-proxy-kustomize-delete", Namespace: "default"}}
		existingConfigMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "migration-proxy-kustomize-delete", Namespace: "default"}}
		Expect(k8sClient.Create(ctx, existingSecret)).To(Succeed())
		Expect(k8sClient.Create(ctx, existingConfigMap)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, existingSecret)
			_ = k8sClient.Delete(ctx, existingConfigMap)
		}()
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, config) }()
		protected := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "migration-proxy-kustomize", Namespace: "default"}}
		Eventually(func() string {
			if createErr := k8sClient.Create(ctx, protected.DeepCopy(), client.DryRunAll); createErr != nil {
				return createErr.Error()
			}
			return ""
		}).Should(ContainSubstring("failed calling webhook"))
		unrelated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{GenerateName: "unrelated-kustomize-", Namespace: "default"}}
		Expect(k8sClient.Create(ctx, unrelated, client.DryRunAll)).To(Succeed())
		for _, object := range []client.Object{existingSecret, existingConfigMap} {
			err := k8sClient.Delete(ctx, object, client.DryRunAll)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed calling webhook"))
		}
		plainPod := corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "example.invalid/test", Env: []corev1.EnvVar{{Name: "OPERATOR_SERVICE_ACCOUNT", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.serviceAccountName"}}}}}}}
		protectedPod := *plainPod.DeepCopy()
		protectedPod.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "migration-proxy-kustomize"}}}}
		protectedVolume := *plainPod.DeepCopy()
		protectedVolume.Volumes = []corev1.Volume{{Name: "credential", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "migration-proxy-kustomize"}}}}}}}}
		protectedInit := *plainPod.DeepCopy()
		protectedInit.InitContainers = []corev1.Container{{Name: "init", Image: "example.invalid/test", Env: []corev1.EnvVar{{Name: "KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "migration-proxy-kustomize"}, Key: "key"}}}}}}
		protectedPull := *plainPod.DeepCopy()
		protectedPull.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "migration-proxy-kustomize"}}
		for _, tc := range []struct {
			name    string
			spec    corev1.PodSpec
			allowed bool
		}{
			{"ordinary", plainPod, true}, {"protected envFrom", protectedPod, false},
			{"protected projection", protectedVolume, false}, {"protected init container", protectedInit, false},
			{"protected image pull secret", protectedPull, false},
		} {
			By(tc.name + " pod, Job and StatefulSet")
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "isolation-pod-", Namespace: "default"}, Spec: *tc.spec.DeepCopy()}
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{GenerateName: "isolation-job-", Namespace: "default"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: *tc.spec.DeepCopy()}}}
			job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{GenerateName: "isolation-sts-", Namespace: "default"}, Spec: appsv1.StatefulSetSpec{ServiceName: "example", Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}}, Spec: *tc.spec.DeepCopy()}}}
			for _, object := range []client.Object{pod, job, sts} {
				err := k8sClient.Create(ctx, object, client.DryRunAll)
				if tc.allowed {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("failed calling webhook"))
				}
			}
		}
		ephemeralPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "isolation-ephemeral-", Namespace: "default"}, Spec: plainPod}
		Expect(k8sClient.Create(ctx, ephemeralPod)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, ephemeralPod) }()
		ephemeralPod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: "debug", Image: "example.invalid/test", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "migration-proxy-kustomize"}}}},
		}}}
		err = k8sClient.SubResource("ephemeralcontainers").Update(ctx, ephemeralPod)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed calling webhook"))
	})
	It("rejects execution records at creation", func() {
		digest := "sha256:" + strings.Repeat("d", 64)
		request := ha.MigrationExecutionRequest{Version: "v1", Stage: "upload", RequestID: digest, OperationUID: "op", SourceID: "source", Target: ha.MigrationTarget{Name: "target", UID: "uid"}, ReportID: "report", ScopeDigest: digest, VerificationDigest: digest, ArchiveRef: &ha.MigrationObjectReference{Name: "archive", UID: "uid"}, ArchiveDigest: digest, ArchivePVCUID: "pvc"}
		operation := celMigrationFixture(request, nil)
		operation.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{request}
		err := k8sClient.Create(ctx, operation)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new migration cannot contain execution records"))
	})

	It("skips unrelated requests when the isolation webhook is unavailable", func() {
		command := exec.Command("helm", "template", "cel-test", filepath.Join("..", "..", "charts", "homeassistant-operator"), "-s", "templates/webhook.yaml")
		rendered, err := command.Output()
		Expect(err).NotTo(HaveOccurred())
		decoder := kyaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
		var config admissionv1.ValidatingWebhookConfiguration
		for {
			var document map[string]interface{}
			err = decoder.Decode(&document)
			if err == io.EOF {
				break
			}
			Expect(err).NotTo(HaveOccurred())
			if document["kind"] == "ValidatingWebhookConfiguration" {
				data, marshalErr := yaml.Marshal(document)
				Expect(marshalErr).NotTo(HaveOccurred())
				Expect(yaml.Unmarshal(data, &config)).To(Succeed())
				break
			}
		}
		Expect(len(config.Webhooks)).To(BeNumerically(">=", 2))
		config.Name = "migration-cel-unavailable-test"
		config.Webhooks = config.Webhooks[:2]
		endpoint := "https://127.0.0.1:1/validate"
		timeout := int32(1)
		for i := range config.Webhooks {
			config.Webhooks[i].ClientConfig.Service = nil
			config.Webhooks[i].ClientConfig.URL = &endpoint
			config.Webhooks[i].TimeoutSeconds = &timeout
		}
		Expect(k8sClient.Create(ctx, &config)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, &config) }()
		protected := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "migration-proxy-cel-test", Namespace: "default"}}
		Eventually(func() string {
			if createErr := k8sClient.Create(ctx, protected.DeepCopy(), client.DryRunAll); createErr != nil {
				return createErr.Error()
			}
			return ""
		}).Should(ContainSubstring("failed calling webhook"))
		unrelated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-unrelated-", Namespace: "default"}}
		Expect(k8sClient.Create(ctx, unrelated)).To(Succeed())
		Expect(k8sClient.Delete(ctx, unrelated)).To(Succeed())
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-unrelated-", Namespace: "default"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "example.invalid/test"}}}}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
		pod = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-protected-", Namespace: "default"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "example.invalid/test", Env: []corev1.EnvVar{{Name: "KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "migration-proxy-cel-test"}, Key: "key"}}}}}}}}
		err = k8sClient.Create(ctx, pod)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed calling webhook"))
		for _, spec := range []corev1.PodSpec{
			{Containers: []corev1.Container{{Name: "test", Image: "example.invalid/test"}}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "migration-proxy-image"}}},
			{Containers: []corev1.Container{{Name: "test", Image: "example.invalid/test"}}, Volumes: []corev1.Volume{{Name: "secret", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "migration-proxy-volume"}}}}},
			{Containers: []corev1.Container{{Name: "test", Image: "example.invalid/test"}}, Volumes: []corev1.Volume{{Name: "secret", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "migration-proxy-project"}}}}}}}}},
			{Containers: []corev1.Container{{Name: "test", Image: "example.invalid/test"}}, InitContainers: []corev1.Container{{Name: "init", Image: "example.invalid/test", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "migration-proxy-env"}}}}}}},
		} {
			candidate := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-protected-", Namespace: "default"}, Spec: spec}
			err = k8sClient.Create(ctx, candidate)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed calling webhook"))
		}
	})
	It("binds restore to the upload source workload", func() {
		digest := "sha256:" + strings.Repeat("c", 64)
		upload := ha.MigrationExecutionRequest{Version: "v1", Stage: "upload", RequestID: digest, OperationUID: "op", SourceID: "source", Target: ha.MigrationTarget{Name: "target", UID: "uid"}, ReportID: "report", ScopeDigest: digest, VerificationDigest: digest, ArchiveRef: &ha.MigrationObjectReference{Name: "archive", UID: "uid"}, ArchiveDigest: digest, ArchivePVCUID: "pvc"}
		restore := *upload.DeepCopy()
		restore.Stage = "restore"
		restore.UploadRequestID = upload.RequestID
		restore.RecoveryKey = &ha.MigrationSecretIdentity{Name: "key", Key: "key", UID: "uid", ResourceVersion: "1"}
		workload := &ha.MigrationSourceWorkload{Kind: "Deployment", Name: "source", UID: "source-uid"}
		for _, tc := range []struct {
			name            string
			upload, restore *ha.MigrationSourceWorkload
			valid           bool
		}{
			{"external", nil, nil, true},
			{"same workload", workload, workload.DeepCopy(), true},
			{"different workload", workload, &ha.MigrationSourceWorkload{Kind: "Deployment", Name: "other", UID: "other"}, false},
			{"missing workload", workload, nil, false},
		} {
			By(tc.name)
			u := *upload.DeepCopy()
			r := *restore.DeepCopy()
			u.SourceWorkload, r.SourceWorkload = tc.upload, tc.restore
			operation := celMigrationFixture(upload, tc.upload)
			Expect(k8sClient.Create(ctx, operation)).To(Succeed())
			operation.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{u, r}
			err := k8sClient.Update(ctx, operation)
			if tc.valid {
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Delete(ctx, operation)).To(Succeed())
			} else {
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("restore must bind accepted upload"))
				Expect(k8sClient.Delete(ctx, operation)).To(Succeed())
			}
		}
	})

	It("blocks new dispatches after abort while preserving existing dispatches", func() {
		digest := "sha256:" + strings.Repeat("b", 64)
		request := ha.MigrationExecutionRequest{Version: "v1", Stage: "upload", RequestID: digest, OperationUID: "op", SourceID: "source", Target: ha.MigrationTarget{Name: "target", UID: "uid"}, ReportID: "report", ScopeDigest: digest, VerificationDigest: digest, ArchiveRef: &ha.MigrationObjectReference{Name: "archive", UID: "uid"}, ArchiveDigest: digest, ArchivePVCUID: "pvc"}
		attempt := ha.MigrationExecutionAttempt{Stage: "upload", RequestID: digest}
		base := celMigrationFixture(request, nil)
		abort := ha.MigrationResolution{Version: "v1", RequestID: digest, Stage: "upload", Decision: "Abort"}
		operation := base.DeepCopy()
		Expect(k8sClient.Create(ctx, operation)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, operation) }()
		operation.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{request}
		operation.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{attempt}
		Expect(k8sClient.Update(ctx, operation)).To(Succeed())
		operation.Spec.Resolutions = []ha.MigrationResolution{abort}
		Expect(k8sClient.Update(ctx, operation)).To(Succeed())
		operation.Spec.ExecutionDispatches = []ha.MigrationExecutionAttempt{attempt}
		err := k8sClient.Update(ctx, operation)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new dispatches cannot be claimed after abort"))
		simultaneous := base.DeepCopy()
		Expect(k8sClient.Create(ctx, simultaneous)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, simultaneous) }()
		simultaneous.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{request}
		simultaneous.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{attempt}
		Expect(k8sClient.Update(ctx, simultaneous)).To(Succeed())
		simultaneous.Spec.Resolutions = []ha.MigrationResolution{abort}
		simultaneous.Spec.ExecutionDispatches = []ha.MigrationExecutionAttempt{attempt}
		err = k8sClient.Update(ctx, simultaneous)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new dispatches cannot be claimed after abort"))

		existing := base.DeepCopy()
		Expect(k8sClient.Create(ctx, existing)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, existing) }()
		existing.Spec.ExecutionRequests = []ha.MigrationExecutionRequest{request}
		existing.Spec.ExecutionAttempts = []ha.MigrationExecutionAttempt{attempt}
		existing.Spec.ExecutionDispatches = []ha.MigrationExecutionAttempt{attempt}
		Expect(k8sClient.Update(ctx, existing)).To(Succeed())
		existing.Spec.Resolutions = []ha.MigrationResolution{abort}
		Expect(k8sClient.Update(ctx, existing)).To(Succeed())
	})

	It("rejects ambiguous observation resolutions on the API server", func() {
		id := "sha256:" + strings.Repeat("a", 64)
		observation := ha.MigrationResolution{
			Version: "v1", Stage: "backup", RequestID: id, Decision: "ObserveExternalResult",
			EvidenceRef: &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: strings.Repeat("a", 64), UID: "uid", Key: "result"},
		}
		operation := &ha.HomeAssistantMigration{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-observation-", Namespace: "default"},
			Spec:       ha.HomeAssistantMigrationSpec{SourceID: "source", Target: ha.MigrationTarget{Name: "target", UID: "uid"}, Resolutions: []ha.MigrationResolution{observation}},
		}
		Expect(k8sClient.Create(ctx, operation)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, operation) }()
		other := observation.DeepCopy()
		other.EvidenceRef.Name = "other"
		operation.Spec.Resolutions = append(operation.Spec.Resolutions, *other)
		err := k8sClient.Update(ctx, operation)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("observation resolution must be unique per request"))
		operation.Spec.Resolutions[1].Stage = "upload"
		Expect(k8sClient.Update(ctx, operation)).To(Succeed())
	})
})

func celMigrationFixture(request ha.MigrationExecutionRequest, workload *ha.MigrationSourceWorkload) *ha.HomeAssistantMigration {
	stop := ha.MigrationApproval{ID: "source-stop", Approved: true, Digest: request.ReportID, TargetUID: request.Target.UID}
	if workload != nil {
		stop.SourceUID = workload.UID
	}
	return &ha.HomeAssistantMigration{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-migration-", Namespace: "default"},
		Spec: ha.HomeAssistantMigrationSpec{
			ExecutionProtocol: "v1", SourceID: request.SourceID, Target: request.Target, ReportID: request.ReportID,
			SourceWorkload: workload, RestoreScopeDigest: request.ScopeDigest,
			Archive:              &ha.MigrationArchive{BackupID: "backup", AgentID: "agent", Digest: request.ArchiveDigest},
			RecoveryKeySecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "key"}, Key: "key"},
			Verification:         []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: request.ScopeDigest}},
			Approvals:            []ha.MigrationApproval{stop, {ID: "restore", Approved: true, Digest: "decision", TargetUID: request.Target.UID, ScopeDigest: request.ScopeDigest, ArchiveDigest: request.ArchiveDigest, VerificationDigest: request.VerificationDigest}},
		},
	}
}

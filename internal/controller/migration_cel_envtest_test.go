package controller

import (
	"bytes"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Migration CEL admission", func() {
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

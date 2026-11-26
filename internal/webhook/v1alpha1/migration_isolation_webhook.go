package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// MigrationIsolationValidator protects the reserved credential namespace from
// principals that could otherwise mount a Secret without having get permission.
// Its ValidatingWebhookConfiguration is intentionally Fail-closed. This is a
// production boundary, unlike the best-effort CR validation webhooks.
type MigrationIsolationValidator struct {
	OperatorUsername string
}

const migrationCredentialPrefix = "migration-proxy-"

func SetupMigrationIsolationWebhookWithManager(mgr ctrl.Manager, operatorUsername string) error {
	if strings.TrimSpace(operatorUsername) == "" {
		return fmt.Errorf("migration isolation operator identity unavailable")
	}
	mgr.GetWebhookServer().Register("/validate-migration-isolation", &admission.Webhook{Handler: &MigrationIsolationValidator{OperatorUsername: operatorUsername}})
	return nil
}

func (v *MigrationIsolationValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create && req.Operation != admissionv1.Update && req.Operation != admissionv1.Delete {
		return admission.Allowed("operation does not affect migration isolation")
	}
	raw := req.Object.Raw
	if req.Operation == admissionv1.Delete {
		raw = req.OldObject.Raw
	}
	var object map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil {
		return admission.Errored(400, fmt.Errorf("cannot validate migration isolation resource"))
	}
	if isReservedCredentialObject(req.Kind.Group, req.Kind.Kind, object) {
		if req.UserInfo.Username != v.OperatorUsername {
			return admission.Denied("migration proxy credentials are managed only by the operator")
		}
		return admission.Allowed("operator manages reserved credential")
	}
	if !objectReferencesReservedCredential(req.Kind.Group, req.Kind.Kind, object) {
		return admission.Allowed("resource does not reference migration credentials")
	}
	if req.Kind.Kind == "Pod" && req.Kind.Group == "" && migrationPodController(req.UserInfo.Username) {
		return admission.Allowed("Kubernetes workload controller materializes an operator-approved pod")
	}
	if req.UserInfo.Username == v.OperatorUsername && (req.Kind.Kind == "Job" || req.Kind.Kind == "StatefulSet") {
		return admission.Allowed("operator creates credential-bound workload")
	}
	return admission.Denied("migration proxy credentials may only be mounted by an operator-created workload")
}

func isReservedCredentialObject(group, kind string, object map[string]any) bool {
	if group != "" || (kind != "Secret" && kind != "ConfigMap") {
		return false
	}
	metadata, _ := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return strings.HasPrefix(name, migrationCredentialPrefix)
}

func objectReferencesReservedCredential(group, kind string, object map[string]any) bool {
	var podSpec map[string]any
	switch {
	case group == "" && kind == "Pod":
		spec, _ := object["spec"].(map[string]any)
		podSpec = spec
	case group == "batch" && kind == "Job", group == "apps" && kind == "StatefulSet":
		spec, _ := object["spec"].(map[string]any)
		template, _ := spec["template"].(map[string]any)
		podSpec, _ = template["spec"].(map[string]any)
	default:
		return false
	}
	return podSpecReferencesReservedCredential(podSpec)
}

func podSpecReferencesReservedCredential(spec map[string]any) bool {
	if spec == nil {
		return false
	}
	if secrets, ok := spec["imagePullSecrets"].([]any); ok {
		for _, item := range secrets {
			if namedReserved(item) {
				return true
			}
		}
	}
	if volumes, ok := spec["volumes"].([]any); ok {
		for _, item := range volumes {
			volume, _ := item.(map[string]any)
			if secret, ok := volume["secret"].(map[string]any); ok && (namedReserved(secret) || secretNameReserved(secret)) {
				return true
			}
			if projected, ok := volume["projected"].(map[string]any); ok {
				if sources, ok := projected["sources"].([]any); ok {
					for _, source := range sources {
						s, _ := source.(map[string]any)
						if secret, ok := s["secret"].(map[string]any); ok && namedReserved(secret) {
							return true
						}
					}
				}
			}
		}
	}
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		containers, _ := spec[field].([]any)
		for _, item := range containers {
			container, _ := item.(map[string]any)
			if refsReservedCredential(container) {
				return true
			}
		}
	}
	return false
}

func refsReservedCredential(container map[string]any) bool {
	if envFrom, ok := container["envFrom"].([]any); ok {
		for _, item := range envFrom {
			e, _ := item.(map[string]any)
			if secret, ok := e["secretRef"].(map[string]any); ok && namedReserved(secret) {
				return true
			}
		}
	}
	if env, ok := container["env"].([]any); ok {
		for _, item := range env {
			e, _ := item.(map[string]any)
			valueFrom, _ := e["valueFrom"].(map[string]any)
			if secret, ok := valueFrom["secretKeyRef"].(map[string]any); ok && namedReserved(secret) {
				return true
			}
		}
	}
	return false
}

func namedReserved(value any) bool {
	object, _ := value.(map[string]any)
	name, _ := object["name"].(string)
	return strings.HasPrefix(name, migrationCredentialPrefix)
}

func secretNameReserved(object map[string]any) bool {
	name, _ := object["secretName"].(string)
	return strings.HasPrefix(name, migrationCredentialPrefix)
}

func migrationPodController(username string) bool {
	return username == "system:serviceaccount:kube-system:job-controller" || username == "system:serviceaccount:kube-system:statefulset-controller"
}

var _ admission.Handler = &MigrationIsolationValidator{}

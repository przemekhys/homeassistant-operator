package controller

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestOperationCRDsAreIncludedInKustomizeInstall(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		Resources []string `json:"resources"`
	}
	if err := yaml.Unmarshal(raw, &configuration); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		"bases/ha.homeassistant.io_homeassistantbackups.yaml",
		"bases/ha.homeassistant.io_homeassistantrestores.yaml",
	} {
		if !slices.Contains(configuration.Resources, file) {
			t.Errorf("operation CRD exists on disk but is absent from make install: %s", file)
		}
	}
}

func TestManagerRoleGrantsOnlyRequiredBackupRestoreResources(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(raw, &role); err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{
		"homeassistantbackups": false, "homeassistantrestores": false,
		"homeassistantbackups/status": false, "homeassistantrestores/status": false,
		"homeassistantbackups/finalizers": false, "homeassistantrestores/finalizers": false,
		"serviceaccounts": false, "roles": false, "rolebindings": false,
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if _, wanted := required[resource]; wanted {
				required[resource] = true
			}
			if resource == "*" {
				t.Fatal("manager role must not use wildcard resources")
			}
		}
	}
	for resource, found := range required {
		if !found {
			t.Errorf("manager role is missing %s", resource)
		}
	}
}

func TestBackupRestoreCRDsExposeStatusAndRequiredSafetyFields(t *testing.T) {
	for file, required := range map[string][]string{
		"ha.homeassistant.io_homeassistantbackups.yaml": {"homeAssistantRef"},
		"ha.homeassistant.io_homeassistantrestores.yaml": {
			"homeAssistantRef", "backupID", "source", "encryptionKeySecretRef",
			"postRestoreAccessSecretRef", "scope", "safetyBackupPolicy",
		},
	} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", file))
			if err != nil {
				t.Fatal(err)
			}
			var crd extensionsv1.CustomResourceDefinition
			if err := yaml.Unmarshal(raw, &crd); err != nil {
				t.Fatal(err)
			}
			if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Subresources == nil ||
				crd.Spec.Versions[0].Subresources.Status == nil {
				t.Fatal("CRD must expose a status subresource")
			}
			properties := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties
			for _, field := range required {
				if _, ok := properties[field]; !ok {
					t.Errorf("spec field %q missing from CRD", field)
				}
			}
		})
	}
}

func TestBackupRestoreWebhooksFailClosed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "webhook", "manifests.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var configuration admissionv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal(raw, &configuration); err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"homeassistantbackups": false, "homeassistantrestores": false}
	for _, webhook := range configuration.Webhooks {
		for _, rule := range webhook.Rules {
			for _, resource := range rule.Resources {
				if _, ok := wanted[resource]; ok {
					if webhook.FailurePolicy == nil || *webhook.FailurePolicy != admissionv1.Fail {
						t.Errorf("%s webhook must fail closed", resource)
					}
					wanted[resource] = true
				}
			}
		}
	}
	for resource, found := range wanted {
		if !found {
			t.Errorf("no validating webhook for %s", resource)
		}
	}
}

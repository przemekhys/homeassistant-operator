package controller

import (
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

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

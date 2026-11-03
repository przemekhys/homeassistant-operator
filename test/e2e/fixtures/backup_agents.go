package fixtures

import (
	"embed"
	"fmt"
)

// BackupAgentFiles is linked into tests only, never the production container.
//
//go:embed backup_test/manifest.json backup_test/__init__.py backup_test/backup.py
var BackupAgentFiles embed.FS

func BackupAgentConfigMap(namespace string) (map[string]any, error) {
	data := make(map[string]string)
	for _, name := range []string{"manifest.json", "__init__.py", "backup.py"} {
		content, err := BackupAgentFiles.ReadFile("backup_test/" + name)
		if err != nil {
			return nil, fmt.Errorf("native agent fixture is unavailable")
		}
		data[name] = string(content)
	}
	return map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]string{"name": "backup-agent-fixture", "namespace": namespace},
		"data":     data,
	}, nil
}

// BackupAgentVolumes is test manifest input, not an operator feature flag.
const BackupAgentVolumes = `  additionalVolumes:
    volumes:
      - name: backup-agent-code
        configMap:
          name: backup-agent-fixture
      - name: backup-agent-data
        emptyDir: {}
    volumeMounts:
      - name: backup-agent-code
        mountPath: /config/custom_components/backup_test
        readOnly: true
      - name: backup-agent-data
        mountPath: /test-backups
`

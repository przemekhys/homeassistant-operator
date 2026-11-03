package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSynchronizationPodProtocol(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to execute the embedded pod-writer protocol")
	}

	for _, test := range []struct {
		name             string
		root             map[string]any
		rotateToken      bool
		wantPermit       bool
		wantAcknowledged bool
	}{
		{
			name:        "acquires a CAS permit and rereads a rotated projected token",
			root:        map[string]any{"mode": "Open", "epoch": 7},
			rotateToken: true,
			wantPermit:  true,
		},
		{
			name:             "acknowledges drain without authorizing a write",
			root:             map[string]any{"mode": "Draining", "epoch": 8},
			wantAcknowledged: true,
		},
		{
			name: "refuses a restarted writer while an earlier dispatch is in flight",
			root: map[string]any{
				"mode": "Open", "epoch": 9,
				"activePermit": map[string]any{"id": "in-flight", "owner": "pod/old/community", "state": "InFlight"},
			},
			wantAcknowledged: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSynchronizationPodProtocol(t, test.root, test.rotateToken)
			if result.permit != test.wantPermit {
				t.Fatalf("permit granted = %t, want %t; output: %s", result.permit, test.wantPermit, result.output)
			}
			if test.rotateToken && !(contains(result.tokens, "first-token") && contains(result.tokens, "rotated-token")) {
				t.Fatalf("protocol did not reread its projected token for each API request: %v", result.tokens)
			}
			root := result.root()
			_, acknowledged := root["drainAcks"]
			if acknowledged != test.wantAcknowledged {
				t.Fatalf("drain acknowledgement = %t, want %t; root: %#v", acknowledged, test.wantAcknowledged, root)
			}
		})
	}
}

// An unrecognised pod identity cannot authorize an ordinary write while the
// instance is draining. Inventory validation for declaring EffectivePause is
// performed by the parent coordinator; this test verifies the pod-side half:
// it obtains no permit and reports only a drain acknowledgement.
func TestSynchronizationPodProtocolUnknownWriterRefusesDrainWrite(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to execute the embedded pod-writer protocol")
	}
	result := runSynchronizationPodProtocol(t, map[string]any{"mode": "Draining", "epoch": 10}, false)
	if result.permit {
		t.Fatalf("unknown draining writer received an ordinary permit: %s", result.output)
	}
	acks, ok := result.root()["drainAcks"].(map[string]any)
	if !ok || len(acks) != 1 {
		t.Fatalf("draining writer did not report its identity for parent inventory validation: %#v", result.root())
	}
}

type synchronizationPodProtocolResult struct {
	output string
	tokens []string
	server *synchronizationPodProtocolServer
	permit bool
}

func (r synchronizationPodProtocolResult) root() map[string]any {
	r.server.mu.Lock()
	defer r.server.mu.Unlock()
	return cloneJSONMap(r.server.root)
}

type synchronizationPodProtocolServer struct {
	mu        sync.Mutex
	root      map[string]any
	version   int
	tokens    []string
	rotated   bool
	tokenFile string
}

func runSynchronizationPodProtocol(t *testing.T, root map[string]any, rotateToken bool) synchronizationPodProtocolResult {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &synchronizationPodProtocolServer{root: cloneJSONMap(root), version: 1, tokenFile: tokenFile}
	server := httptest.NewTLSServer(api)
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, ok := strings.Cut(endpoint.Host, ":")
	if !ok {
		t.Fatalf("TLS test endpoint has no port: %q", endpoint.Host)
	}

	script := strings.Replace(synchronizationWriterProtocolScript,
		"context = ssl.create_default_context(cafile=SYNC_CA_PATH)", "context = ssl._create_unverified_context()", 1)
	script = strings.Replace(script, "SYNC_TOKEN_PATH = '/var/run/secrets/ha-coordination/token'",
		fmt.Sprintf("SYNC_TOKEN_PATH = %q", tokenFile), 1)
	script += "\npermit = sync_acquire()\nprint('permit=' + str(bool(permit)))\n"
	cmd := exec.Command("python3", "-c", script)
	cmd.Env = append(os.Environ(),
		"HA_SYNC_COORDINATION_CONFIGMAP=instance-ha-coordination",
		"POD_NAMESPACE=default",
		"POD_UID=current-pod",
		"HA_SYNC_WRITER=pod/current-pod/community-repository-sidecar",
		"KUBERNETES_SERVICE_HOST="+host,
		"KUBERNETES_SERVICE_PORT_HTTPS="+port,
	)
	if rotateToken {
		api.rotated = true
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("embedded protocol failed: %v\n%s", err, output)
	}
	return synchronizationPodProtocolResult{
		output: string(output), tokens: append([]string(nil), api.tokens...), server: api,
		permit: strings.Contains(string(output), "permit=True"),
	}
}

func (s *synchronizationPodProtocolServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		if s.rotated {
			s.rotated = false
			if err := os.WriteFile(s.tokenFile, []byte("rotated-token"), 0o600); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		s.writeConfigMap(w)
	case http.MethodPut:
		var configMap map[string]any
		if err := json.NewDecoder(r.Body).Decode(&configMap); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		metadata, _ := configMap["metadata"].(map[string]any)
		if metadata["resourceVersion"] != fmt.Sprint(s.version) {
			http.Error(w, "resource version conflict", http.StatusConflict)
			return
		}
		data, _ := configMap["data"].(map[string]any)
		raw, _ := data["coordination.json"].(string)
		if err := json.Unmarshal([]byte(raw), &s.root); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.version++
		s.writeConfigMap(w)
	default:
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}
}

func (s *synchronizationPodProtocolServer) writeConfigMap(w http.ResponseWriter) {
	raw, err := json.Marshal(s.root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"metadata": map[string]any{"resourceVersion": fmt.Sprint(s.version)},
		"data":     map[string]any{"coordination.json": string(raw)},
	})
}

func cloneJSONMap(in map[string]any) map[string]any {
	raw, _ := json.Marshal(in)
	out := make(map[string]any)
	_ = json.Unmarshal(raw, &out)
	return out
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestBackupConfigFailureCanaryDoesNotLeakFromStatusEventsOrLogSafeError(t *testing.T) {
	const canary = "backup-key-canary-must-not-escape"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var auth map[string]any
		if conn.ReadJSON(&auth) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
		var command map[string]any
		if conn.ReadJSON(&command) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"id": command["id"], "type": "result", "success": false,
			"error": map[string]string{"code": "invalid_password", "message": canary},
		})
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default", UID: types.UID("home-uid")},
		Spec:       hav1.HomeAssistantSpec{Backup: &hav1.BackupSpec{Enabled: true}},
	}
	token := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "home-homeassistant-api-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("api-token-canary-must-not-escape")},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha, token).Build()
	recorder := events.NewFakeRecorder(1)
	reconciler := &HomeAssistantReconciler{
		Client:    client,
		APIReader: client,
		Scheme:    scheme,
		Recorder:  recorder,
		NewHAClient: func(string) *haclient.Client {
			return haclient.NewClient("ws" + strings.TrimPrefix(server.URL, "http"))
		},
	}

	if _, err := reconciler.reconcileBackupConfig(context.Background(), ha); err != nil {
		t.Fatal(err)
	}

	updated := &hav1.HomeAssistant{}
	key := types.NamespacedName{Namespace: ha.Namespace, Name: ha.Name}
	if err := client.Get(context.Background(), key, updated); err != nil {
		t.Fatal(err)
	}
	statusJSON, err := json.Marshal(updated.Status)
	if err != nil {
		t.Fatal(err)
	}
	crJSON, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-recorder.Events:
		assertBackupCanaryAbsent(t, event, canary, string(token.Data["token"]))
	case <-time.After(time.Second):
		t.Fatal("expected backup configuration failure event")
	}
	assertBackupCanaryAbsent(t, string(statusJSON), canary, string(token.Data["token"]))
	assertBackupCanaryAbsent(t, string(crJSON), canary, string(token.Data["token"]))

	logSafeError := backupConfigErrorMessage("get", &haclient.BackupError{
		Kind: haclient.BackupErrorPasswordIncorrect,
		Err:  errors.New(canary),
	})
	assertBackupCanaryAbsent(t, logSafeError, canary, string(token.Data["token"]))
}

func assertBackupCanaryAbsent(t *testing.T, output string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(output, canary) {
			t.Fatalf("backup secret canary leaked: %q in %q", canary, output)
		}
	}
}

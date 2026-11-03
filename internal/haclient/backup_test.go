package haclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

func backupWebSocketServer(t *testing.T, reply func(map[string]interface{}) map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(map[string]interface{}{"type": "auth_required"}); err != nil {
			return
		}
		var auth map[string]interface{}
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]interface{}{"type": "auth_ok"}); err != nil {
			return
		}
		var command map[string]interface{}
		if err := conn.ReadJSON(&command); err != nil {
			return
		}
		response := reply(command)
		response["id"] = command["id"]
		response["type"] = "result"
		if err := conn.WriteJSON(response); err != nil {
			t.Errorf("reply: %v", err)
		}
	}))
}

func backupClient(server *httptest.Server) *Client {
	return NewClient("ws" + strings.TrimPrefix(server.URL, "http"))
}

func TestListBackupAgentsDecodesNativeResult(t *testing.T) {
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		if command["type"] != "backup/agents/info" {
			t.Errorf("type = %v", command["type"])
		}
		return map[string]interface{}{
			"success": true,
			"result": map[string]interface{}{
				"agents": []map[string]interface{}{{"agent_id": "core", "name": "Home Assistant Core"}},
			},
		}
	})
	defer server.Close()
	agents, err := backupClient(server).ListBackupAgents(context.Background(), "token")
	if err != nil || len(agents) != 1 || agents[0].ID != "core" {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
}

func TestGetBackupConfigDecodesResultEnvelopeWithoutExposingPassword(t *testing.T) {
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		if command["type"] != "backup/config/info" {
			t.Errorf("type = %v", command["type"])
		}
		return map[string]interface{}{
			"success": true,
			"result": map[string]interface{}{"config": map[string]interface{}{
				"schedule":  map[string]interface{}{"recurrence": "daily"},
				"retention": map[string]interface{}{"copies": 3},
				"agents": map[string]interface{}{
					"backup.local": map[string]interface{}{"protected": true},
					"community":    map[string]interface{}{"protected": false},
				},
				"create_backup": map[string]interface{}{
					"agent_ids": []string{"backup.local", "community"},
					"password":  "synthetic-backup-password",
				},
			}},
		}
	})
	defer server.Close()

	config, err := backupClient(server).GetBackupConfig(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.CreateBackup.AgentIDs) != 2 || config.CreateBackup.AgentIDs[1] != "community" {
		t.Fatalf("agents = %#v", config.CreateBackup.AgentIDs)
	}
	if config.CreateBackup.Password == nil || *config.CreateBackup.Password != "synthetic-backup-password" {
		t.Fatal("password was not retained in the private client result")
	}
	if !config.Agents["backup.local"].Protected || config.Agents["community"].Protected {
		t.Fatalf("agent protection = %#v", config.Agents)
	}
}

func TestGetBackupConfigRejectsMissingNativeEnvelope(t *testing.T) {
	for _, result := range []any{
		nil, map[string]any{}, map[string]any{"config": nil},
		map[string]any{"create_backup": map[string]any{"password": "synthetic-config-canary"}},
		map[string]any{"config": "synthetic-config-canary"},
	} {
		server := backupWebSocketServer(t, func(map[string]interface{}) map[string]interface{} {
			return map[string]interface{}{"success": true, "result": result}
		})
		config, err := backupClient(server).GetBackupConfig(context.Background(), "token")
		server.Close()
		if err == nil || config != nil {
			t.Fatal("missing or malformed config envelope was accepted")
		}
		if strings.Contains(err.Error(), "synthetic-config-canary") {
			t.Fatal("malformed config error disclosed response data")
		}
	}
}

func TestBackupErrorClassificationIsBounded(t *testing.T) {
	for code, want := range map[string]BackupErrorKind{
		"busy":             BackupErrorBusyBeforeAcceptance,
		"invalid_password": BackupErrorPasswordIncorrect,
		"unknown_command":  BackupErrorUnsupportedCapability,
		"unrecognized":     BackupErrorDispatchUnknown,
	} {
		t.Run(code, func(t *testing.T) {
			err := classifyBackupError(code, errors.New("synthetic-secret"))
			var backupErr *BackupError
			if !errors.As(err, &backupErr) || backupErr.Kind != want {
				t.Fatalf("error = %#v, want %s", err, want)
			}
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("safe error leaked input: %v", err)
			}
		})
	}
}

func TestParseBackupEventHandlesKnownVariantsAndIgnoresOtherEvents(t *testing.T) {
	event, recognized, err := parseBackupEvent(
		json.RawMessage(
			`{"type":"event","event":{"event_type":"backup_progress","data":{
				"backup_id":"archive","backup_job_id":"job","agent_id":"backup.local","stage":"upload"}}}`,
		),
	)
	if err != nil || !recognized {
		t.Fatalf("recognized=%v err=%v", recognized, err)
	}
	if event.BackupID != "archive" || event.BackupJobID != "job" || event.AgentID != "backup.local" ||
		event.Stage != "upload" {
		t.Fatalf("event = %#v", event)
	}
	unrelated := json.RawMessage(`{"type":"event","event":{"event_type":"state_changed","data":{}}}`)
	if _, recognized, err := parseBackupEvent(unrelated); err != nil || recognized {
		t.Fatalf("unrelated event recognized=%v err=%v", recognized, err)
	}
}

func TestMergeBackupConfigPreservesPasswordAndNormalizesAgents(t *testing.T) {
	password := "synthetic-password"
	includeDatabase := true
	observed := BackupConfig{CreateBackup: BackupCreateConfig{Password: &password, AgentIDs: []string{"backup.local"}}}
	merged := MergeBackupConfig(observed, BackupConfigRequest{CreateBackup: &BackupCreateConfig{
		IncludeDatabase: &includeDatabase,
		AgentIDs:        []string{"remote", "backup.local", "remote"},
	}})
	if merged.CreateBackup.Password == nil || *merged.CreateBackup.Password != password {
		t.Fatal("merge discarded Home Assistant-owned password")
	}
	if got := strings.Join(merged.CreateBackup.AgentIDs, ","); got != "backup.local,remote" {
		t.Fatalf("agents = %q", got)
	}
	if !BackupConfigEqual(
		merged,
		BackupConfig{
			CreateBackup: BackupCreateConfig{
				Password:        &password,
				IncludeDatabase: &includeDatabase,
				AgentIDs:        []string{"remote", "backup.local"},
			},
		},
	) {
		t.Fatal("set-equivalent agent configurations differ")
	}
}

func TestGetBackupDetailsReturnsSafePerAgentResults(t *testing.T) {
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		if command["type"] != "backup/details" || command["backup_id"] != "archive" {
			t.Errorf("command = %#v", command)
		}
		return map[string]interface{}{"success": true, "result": map[string]interface{}{
			"backup": map[string]interface{}{
				"backup_id":        "archive",
				"agents":           map[string]interface{}{"backup.local": map[string]interface{}{"protected": true}},
				"failed_agent_ids": []string{"remote"},
			},
		}}
	})
	defer server.Close()
	details, err := backupClient(server).GetBackupDetails(context.Background(), "token", "archive")
	if err != nil || len(details.Agents) != 2 || !details.Agents[0].Protected ||
		details.Agents[1].Error != BackupErrorSourceUnavailable {
		t.Fatalf("details=%#v err=%v", details, err)
	}
}

func TestReconcileBackupConfigWritesOnlyOnDiff(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
		var auth map[string]interface{}
		if conn.ReadJSON(&auth) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})
		var command map[string]interface{}
		if conn.ReadJSON(&command) != nil {
			return
		}
		calls++
		if command["type"] == "backup/config/info" {
			_ = conn.WriteJSON(
				map[string]interface{}{
					"id":      command["id"],
					"type":    "result",
					"success": true,
					"result": map[string]interface{}{
						"config": map[string]interface{}{
							"schedule":  map[string]interface{}{"recurrence": "daily"},
							"retention": map[string]interface{}{},
							"create_backup": map[string]interface{}{
								"agent_ids": []string{"backup.local"},
								"password":  "synthetic-password",
							},
						},
					},
				},
			)
			return
		}
		if command["type"] != "backup/config/update" {
			t.Errorf("command = %#v", command)
		}
		_ = conn.WriteJSON(
			map[string]interface{}{
				"id":      command["id"],
				"type":    "result",
				"success": true,
				"result":  map[string]interface{}{},
			},
		)
	}))
	defer server.Close()

	local := BackupConfigRequest{CreateBackup: &BackupCreateConfig{AgentIDs: []string{"backup.local"}}}
	changed, err := backupClient(server).ReconcileBackupConfig(context.Background(), "token", local)
	if err != nil || changed || calls != 1 {
		t.Fatalf("unchanged: changed=%v calls=%d err=%v", changed, calls, err)
	}
	remote := BackupConfigRequest{CreateBackup: &BackupCreateConfig{AgentIDs: []string{"remote"}}}
	changed, err = backupClient(server).ReconcileBackupConfig(context.Background(), "token", remote)
	if err != nil || !changed || calls != 3 {
		t.Fatalf("changed: changed=%v calls=%d err=%v", changed, calls, err)
	}
}

func TestSubscribeBackupEventsReconnectsOnlyTheSubscription(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		connection := connections.Add(1)
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
		var auth map[string]interface{}
		if conn.ReadJSON(&auth) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})
		var subscription map[string]interface{}
		if conn.ReadJSON(&subscription) != nil {
			return
		}
		if subscription["type"] != "backup/subscribe_events" {
			t.Errorf("subscription type = %v", subscription["type"])
			return
		}
		_ = conn.WriteJSON(
			map[string]interface{}{"id": subscription["id"], "type": "result", "success": true, "result": nil},
		)
		if connection == 2 {
			_ = conn.WriteJSON(
				map[string]interface{}{
					"type": "event",
					"event": map[string]interface{}{
						"event_type": "backup_done",
						"data":       map[string]interface{}{"backup_id": "archive"},
					},
				},
			)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var received BackupEvent
	err := backupClient(server).SubscribeBackupEvents(ctx, "token", func(event BackupEvent) {
		received = event
		cancel()
	})
	if err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 2 || received.BackupID != "archive" || received.Type != "backup_done" {
		t.Fatalf("connections=%d event=%#v", connections.Load(), received)
	}
}

func TestGenerateBackupUsesSingleNativeCommandAndClassifiesBusy(t *testing.T) {
	calls := 0
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		calls++
		if command["type"] != "backup/generate" || command["password"] != "secret" {
			t.Errorf("unexpected command: %#v", command)
		}
		return map[string]interface{}{
			"success": false,
			"error":   map[string]interface{}{"code": "busy", "message": "redacted by adapter"},
		}
	})
	defer server.Close()
	request := GenerateBackupRequest{
		AgentIDs: []string{"core"}, IncludeDatabase: true, Password: "secret", KeyProtectionVerified: true,
	}
	_, err := backupClient(server).GenerateBackup(context.Background(), "token", request)
	var backupErr *BackupError
	if !errors.As(err, &backupErr) || backupErr.Kind != BackupErrorBusyBeforeAcceptance {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("native command count = %d, want 1", calls)
	}
}

func TestGenerateBackupClassifiesSafeNativeErrors(t *testing.T) {
	for name, response := range map[string]map[string]interface{}{
		"password": {"code": "invalid_password", "want": string(BackupErrorPasswordIncorrect)},
		"unknown":  {"code": "unknown_command", "want": string(BackupErrorUnsupportedCapability)},
	} {
		t.Run(name, func(t *testing.T) {
			server := backupWebSocketServer(t, func(map[string]interface{}) map[string]interface{} {
				return map[string]interface{}{
					"success": false,
					"error":   map[string]interface{}{"code": response["code"], "message": "synthetic-secret"},
				}
			})
			defer server.Close()
			request := GenerateBackupRequest{Password: "synthetic-secret", KeyProtectionVerified: true}
			_, err := backupClient(server).GenerateBackup(context.Background(), "token", request)
			var backupErr *BackupError
			if !errors.As(err, &backupErr) || string(backupErr.Kind) != response["want"] {
				t.Fatalf("err=%v", err)
			}
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("safe error leaked input: %v", err)
			}
		})
	}
}

func TestRestoreDisconnectIsUnknownAndIsNotRetried(t *testing.T) {
	calls := 0
	server := backupWebSocketServer(t, func(command map[string]interface{}) map[string]interface{} {
		calls++
		return map[string]interface{}{} // close after an invalid response: caller receives no accepted result.
	})
	defer server.Close()
	request := RestoreBackupRequest{
		BackupID: "archive", AgentID: "core", Password: "secret", RestoreHomeAssistant: true,
	}
	err := backupClient(server).RestoreBackup(context.Background(), "token", request)
	var backupErr *BackupError
	if !errors.As(err, &backupErr) || backupErr.Kind != BackupErrorDispatchUnknown {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("native command count = %d, want 1", calls)
	}
}

func TestBackupErrorsDoNotRevealPassword(t *testing.T) {
	err := classifyBackupError("invalid_password", errors.New("secret-value"))
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("safe error leaked secret: %v", err)
	}
}

func TestGenerateBackupRefusesUnverifiedKeyProtectionBeforeDispatch(t *testing.T) {
	calls := 0
	server := backupWebSocketServer(t, func(map[string]interface{}) map[string]interface{} {
		calls++
		return map[string]interface{}{"success": true, "result": map[string]interface{}{"backup_job_id": "unexpected"}}
	})
	defer server.Close()

	_, err := backupClient(
		server,
	).GenerateBackup(context.Background(), "token", GenerateBackupRequest{Password: "synthetic-password"})
	var backupErr *BackupError
	if !errors.As(err, &backupErr) || backupErr.Kind != BackupErrorPasswordIncorrect {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("native command count = %d, want 0", calls)
	}
}

func TestBackupAdapterRejectsUntrustedTLSWithoutLeakingCredentials(t *testing.T) {
	const token = "backup-token-canary-must-not-escape"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("untrusted TLS endpoint must not complete a backup request")
	}))
	defer server.Close()

	_, err := NewClient("wss"+strings.TrimPrefix(server.URL, "https")).ListBackupAgents(context.Background(), token)
	if err == nil {
		t.Fatal("untrusted TLS endpoint was accepted")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("TLS error leaked token: %v", err)
	}
	var backupErr *BackupError
	if !errors.As(err, &backupErr) || backupErr.Kind != BackupErrorDispatchUnknown {
		t.Fatalf("error = %#v, want safe dispatch-unknown error", err)
	}
}

func TestBackupAdapterRejectsWebSocketRedirectWithoutFollowingIt(t *testing.T) {
	const token = "backup-token-canary-must-not-escape"
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		t.Error("backup adapter followed an untrusted websocket redirect")
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/websocket", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	_, err := backupClient(redirect).ListBackupAgents(context.Background(), token)
	if err == nil {
		t.Fatal("websocket redirect was accepted")
	}
	if targetCalls != 0 {
		t.Fatalf("redirect target received %d requests", targetCalls)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("redirect error leaked token: %v", err)
	}
	var backupErr *BackupError
	if !errors.As(err, &backupErr) || backupErr.Kind != BackupErrorDispatchUnknown {
		t.Fatalf("error = %#v, want safe dispatch-unknown error", err)
	}
}

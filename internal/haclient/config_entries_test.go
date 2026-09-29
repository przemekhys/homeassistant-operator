package haclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUploadFile(t *testing.T) {
	data := []byte{0x00, 0x01, 0xff, 'p', 'e', 'm'}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/file_upload" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authorization header")
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("read uploaded file: %v", err)
		}
		defer func() { _ = file.Close() }()
		got, _ := io.ReadAll(file)
		if string(got) != string(data) || header.Filename != "client.crt" {
			t.Errorf("uploaded file did not preserve name or bytes")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"file_id": "file-123"})
	}))
	defer server.Close()

	fileID, err := NewClient(server.URL).UploadFile(context.Background(), "test-token", "client.crt", data)
	if err != nil || fileID != "file-123" {
		t.Fatalf("UploadFile() = %q, %v", fileID, err)
	}
}

func TestUploadFileRejectsMissingIDAndCleanupIgnoresNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]string{})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL)
	if _, err := client.UploadFile(context.Background(), "token", "ca.crt", []byte("ca")); err == nil {
		t.Fatal("expected missing file_id error")
	}
	if err := client.DeleteUploadedFile(context.Background(), "token", "gone"); err != nil {
		t.Fatalf("DeleteUploadedFile() = %v", err)
	}
}

func TestListConfigEntries(t *testing.T) {
	entries := []ConfigEntry{
		{EntryID: "abc123", Domain: "mqtt", Title: "MQTT", State: "loaded", Source: "user"},
		{EntryID: "def456", Domain: "zha", Title: "ZHA", State: "loaded", Source: "user"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config/config_entries/entry" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing or wrong Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	result, err := client.ListConfigEntries(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result[0].Domain != "mqtt" {
		t.Errorf("expected domain mqtt, got %s", result[0].Domain)
	}
}

func TestIsIntegrationConfigured(t *testing.T) {
	entries := []ConfigEntry{
		{EntryID: "abc123", Domain: "mqtt", Title: "MQTT"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := NewClient(server.URL)

	found, err := client.IsIntegrationConfigured(context.Background(), "token", "mqtt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected mqtt to be configured")
	}

	found, err = client.IsIntegrationConfigured(context.Background(), "token", "zha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected zha to not be configured")
	}
}

func TestStartConfigFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config/config_entries/flow" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["handler"] != "mqtt" {
			t.Errorf("expected handler=mqtt, got %v", body["handler"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FlowResponse{
			FlowID: "flow-123",
			Type:   "form",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.StartConfigFlow(context.Background(), "token", "mqtt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FlowID != "flow-123" {
		t.Errorf("expected flow_id=flow-123, got %s", resp.FlowID)
	}
}

func TestSubmitConfigFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/config/config_entries/flow/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FlowResponse{
			FlowID: "flow-123",
			Type:   "create_entry",
			Title:  "MQTT",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.SubmitConfigFlow(context.Background(), "token", "flow-123", map[string]interface{}{
		"broker": "mosquitto.default.svc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Type != "create_entry" {
		t.Errorf("expected type=create_entry, got %s", resp.Type)
	}
}

func TestSubmitConfigFlowUntilDone_SingleStep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FlowResponse{
			FlowID: "flow-123",
			Type:   "create_entry",
			Title:  "MQTT",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.SubmitConfigFlowUntilDone(context.Background(), "token", "flow-123",
		[]map[string]interface{}{{"broker": "mosquitto.default.svc"}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Type != "create_entry" {
		t.Errorf("expected type=create_entry, got %s", resp.Type)
	}
}

func TestSubmitConfigFlowUntilDone_MultiStep(t *testing.T) {
	var step atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := step.Add(1)
		w.Header().Set("Content-Type", "application/json")
		flowType := "form"
		if current == 2 {
			flowType = "create_entry"
		}
		_ = json.NewEncoder(w).Encode(FlowResponse{FlowID: "flow-multi", Type: flowType})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.SubmitConfigFlowUntilDone(context.Background(), "token", "flow-multi",
		[]map[string]interface{}{
			{"broker": "mosquitto.default.svc"},
			{"port": 1883},
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Type != "create_entry" {
		t.Errorf("expected type=create_entry, got %s", resp.Type)
	}
	if step.Load() != 2 {
		t.Errorf("expected 2 HTTP calls, got %d", step.Load())
	}
}

func TestSubmitConfigFlowUntilDone_Abort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FlowResponse{FlowID: "flow-123", Type: "abort"})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.SubmitConfigFlowUntilDone(context.Background(), "token", "flow-123",
		[]map[string]interface{}{{"broker": "bad-host"}},
	)
	if err == nil {
		t.Fatal("expected error for aborted flow, got nil")
	}
}

func TestSubmitConfigFlowUntilDone_StepsExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Always return "form" — never reaches create_entry
		_ = json.NewEncoder(w).Encode(FlowResponse{FlowID: "flow-123", Type: "form"})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.SubmitConfigFlowUntilDone(context.Background(), "token", "flow-123",
		[]map[string]interface{}{{"step1": "data"}},
	)
	if err == nil {
		t.Fatal("expected error when steps exhausted, got nil")
	}
}

func TestRemoveConfigEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	err := client.RemoveConfigEntry(context.Background(), "token", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemoveConfigEntry_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	err := client.RemoveConfigEntry(context.Background(), "token", "nonexistent")
	if err != nil {
		t.Fatalf("expected nil for 404, got: %v", err)
	}
}

func TestParseConfigEntryResult(t *testing.T) {
	t.Run("parses valid create_entry result", func(t *testing.T) {
		result, _ := json.Marshal(map[string]interface{}{
			"entry_id": "abc123",
			"domain":   "mqtt",
			"title":    "MQTT",
		})
		resp := &FlowResponse{
			Type:   "create_entry",
			Result: json.RawMessage(result),
		}
		got, err := ParseConfigEntryResult(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.EntryID != "abc123" {
			t.Errorf("expected entry_id=abc123, got %s", got.EntryID)
		}
		if got.Domain != "mqtt" {
			t.Errorf("expected domain=mqtt, got %s", got.Domain)
		}
	})

	t.Run("returns error for non-create_entry type", func(t *testing.T) {
		resp := &FlowResponse{Type: "form"}
		_, err := ParseConfigEntryResult(resp)
		if err == nil {
			t.Fatal("expected error for non-create_entry type")
		}
	})

	t.Run("returns error for missing entry_id", func(t *testing.T) {
		result, _ := json.Marshal(map[string]interface{}{
			"domain": "mqtt",
		})
		resp := &FlowResponse{
			Type:   "create_entry",
			Result: json.RawMessage(result),
		}
		_, err := ParseConfigEntryResult(resp)
		if err == nil {
			t.Fatal("expected error for missing entry_id")
		}
	})

	t.Run("returns error for nil result", func(t *testing.T) {
		resp := &FlowResponse{Type: "create_entry", Result: nil}
		_, err := ParseConfigEntryResult(resp)
		if err == nil {
			t.Fatal("expected error for nil result")
		}
	})
}

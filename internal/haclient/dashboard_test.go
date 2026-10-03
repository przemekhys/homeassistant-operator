package haclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDashboardOperations(t *testing.T) {
	dashboard := Dashboard{
		URLPath:       "energy-overview",
		Title:         "Energy Overview",
		Icon:          "mdi:lightning-bolt",
		ShowInSidebar: true,
		RequireAdmin:  true,
	}

	tests := []struct {
		name        string
		commandType string
		result      interface{}
		call        func(*Client) error
		assert      func(*testing.T, map[string]interface{})
	}{
		{
			name:        "list",
			commandType: "lovelace/dashboards/list",
			result: []map[string]interface{}{{
				"id": "dashboard-1", "url_path": dashboard.URLPath, "mode": "storage", "title": dashboard.Title,
			}},
			call: func(client *Client) error {
				dashboards, err := client.ListDashboards(context.Background(), "test-token")
				if err == nil && (len(dashboards) != 1 || dashboards[0].ID != "dashboard-1") {
					t.Fatal("ListDashboards() did not return the dashboard")
				}
				return err
			},
			assert: func(t *testing.T, command map[string]interface{}) {
				if _, ok := command["url_path"]; ok {
					t.Error("list command included dashboard data")
				}
			},
		},
		{
			name:        "create storage dashboard",
			commandType: "lovelace/dashboards/create",
			result: map[string]interface{}{
				"id": "dashboard-1", "url_path": dashboard.URLPath, "mode": "storage", "title": dashboard.Title,
			},
			call: func(client *Client) error {
				created, err := client.CreateDashboard(context.Background(), "test-token", dashboard)
				if err == nil && (created.ID != "dashboard-1" || created.Mode != DashboardModeStorage) {
					t.Fatal("CreateDashboard() did not return the created storage dashboard")
				}
				return err
			},
			assert: assertCreateDashboardCommand(dashboard),
		},
		{
			name:        "update storage dashboard",
			commandType: "lovelace/dashboards/update",
			result: map[string]interface{}{
				"id": "dashboard-1", "url_path": dashboard.URLPath, "mode": "storage", "title": dashboard.Title,
			},
			call: func(client *Client) error {
				_, err := client.UpdateDashboard(context.Background(), "test-token", "dashboard-1", dashboard)
				return err
			},
			assert: func(t *testing.T, command map[string]interface{}) {
				if command["dashboard_id"] != "dashboard-1" {
					t.Errorf("dashboard_id = %q, want dashboard-1", command["dashboard_id"])
				}
				assertDashboardMetadata(dashboard)(t, command)
				if _, ok := command["url_path"]; ok {
					t.Error("update command included immutable url_path")
				}
			},
		},
		{
			name:        "get config",
			commandType: "lovelace/config",
			result:      map[string]interface{}{"title": dashboard.Title, "views": []interface{}{}},
			call: func(client *Client) error {
				config, err := client.GetDashboardConfig(context.Background(), "test-token", dashboard.URLPath)
				if err == nil && !json.Valid(config) {
					t.Fatal("GetDashboardConfig() returned invalid JSON")
				}
				return err
			},
			assert: assertURLPath(dashboard.URLPath),
		},
		{
			name:        "save config",
			commandType: "lovelace/config/save",
			result:      map[string]interface{}{},
			call: func(client *Client) error {
				return client.SaveDashboardConfig(context.Background(), "test-token", dashboard.URLPath,
					json.RawMessage(`{"title":"Energy Overview","views":[]}`))
			},
			assert: func(t *testing.T, command map[string]interface{}) {
				assertURLPath(dashboard.URLPath)(t, command)
				config, ok := command["config"].(map[string]interface{})
				if !ok || config["title"] != dashboard.Title {
					t.Errorf("unexpected saved config: %#v", command["config"])
				}
			},
		},
		{
			name:        "delete",
			commandType: "lovelace/dashboards/delete",
			result:      map[string]interface{}{},
			call: func(client *Client) error {
				return client.DeleteDashboard(context.Background(), "test-token", "dashboard-1")
			},
			assert: func(t *testing.T, command map[string]interface{}) {
				if command["dashboard_id"] != "dashboard-1" {
					t.Errorf("dashboard_id = %q, want dashboard-1", command["dashboard_id"])
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newDashboardTestClient(t, test.commandType, test.result, test.assert)
			if err := test.call(client); err != nil {
				t.Fatalf("dashboard operation failed: %v", err)
			}
		})
	}
}

func TestListDashboardsReturnsHomeAssistantError(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
		var auth map[string]interface{}
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})
		var command map[string]interface{}
		_ = conn.ReadJSON(&command)
		_ = conn.WriteJSON(map[string]interface{}{
			"id": command["id"], "type": "result", "success": false,
			"error": map[string]interface{}{"code": "not_found", "message": "Dashboard not found"},
		})
	}))
	defer server.Close()

	_, err := NewClient("ws"+strings.TrimPrefix(server.URL, "http")).ListDashboards(context.Background(), "test-token")
	if err == nil || !strings.Contains(err.Error(), "Dashboard not found") {
		t.Fatalf("ListDashboards() error = %v, want Home Assistant error", err)
	}
}

func newDashboardTestClient(
	t *testing.T,
	expectedType string,
	result interface{},
	assert func(*testing.T, map[string]interface{}),
) *Client {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
		var auth map[string]interface{}
		if err := conn.ReadJSON(&auth); err != nil || auth["access_token"] != "test-token" {
			t.Errorf("unexpected authentication message: %#v, %v", auth, err)
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

		var command map[string]interface{}
		if err := conn.ReadJSON(&command); err != nil {
			t.Errorf("read dashboard command: %v", err)
			return
		}
		if command["type"] != expectedType {
			t.Errorf("command type = %q, want %q", command["type"], expectedType)
		}
		assert(t, command)
		_ = conn.WriteJSON(map[string]interface{}{
			"id": command["id"], "type": "result", "success": true, "result": result,
		})
	}))
	t.Cleanup(server.Close)
	return NewClient("ws" + strings.TrimPrefix(server.URL, "http"))
}

func assertCreateDashboardCommand(dashboard Dashboard) func(*testing.T, map[string]interface{}) {
	return func(t *testing.T, command map[string]interface{}) {
		if command["url_path"] != dashboard.URLPath {
			t.Errorf("unexpected dashboard command: %#v", command)
		}
		assertDashboardMetadata(dashboard)(t, command)
	}
}

func assertDashboardMetadata(dashboard Dashboard) func(*testing.T, map[string]interface{}) {
	return func(t *testing.T, command map[string]interface{}) {
		if command["title"] != dashboard.Title || command["icon"] != dashboard.Icon {
			t.Errorf("unexpected dashboard metadata: %#v", command)
		}
		if command["show_in_sidebar"] != dashboard.ShowInSidebar || command["require_admin"] != dashboard.RequireAdmin {
			t.Errorf("unexpected dashboard access settings: %#v", command)
		}
	}
}

func assertURLPath(urlPath string) func(*testing.T, map[string]interface{}) {
	return func(t *testing.T, command map[string]interface{}) {
		if command["url_path"] != urlPath {
			t.Errorf("url_path = %q, want %q", command["url_path"], urlPath)
		}
	}
}

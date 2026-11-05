package utils

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalTodoFixtureHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authorization")
		}
		switch r.URL.Path {
		case "/api/states":
			_, _ = io.WriteString(w, `[{"entity_id":"todo.recovery","state":"0","attributes":{"friendly_name":"Recovery list"}}]`)
		case "/api/services/todo/add_item":
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"entity_id":"todo.recovery","item":"control"}` {
				t.Errorf("unexpected item request: %s", body)
			}
			w.WriteHeader(http.StatusOK)
		case "/api/todo/todo.recovery":
			_, _ = io.WriteString(w, `[{"summary":"control","status":"needs_action"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	states, err := ListHAStates(context.Background(), server.Client(), server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	entityID, found := LocalTodoEntity(states, "Recovery list")
	if !found || entityID != "todo.recovery" {
		t.Fatalf("LocalTodoEntity() = %q, %t", entityID, found)
	}
	if err := AddLocalTodoItem(context.Background(), server.Client(), server.URL, "test-token", entityID, "control"); err != nil {
		t.Fatal(err)
	}
	hasItem, err := LocalTodoHasItem(context.Background(), server.Client(), server.URL, "test-token", entityID, "control")
	if err != nil || !hasItem {
		t.Fatalf("LocalTodoHasItem() = %t, %v", hasItem, err)
	}
}

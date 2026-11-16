package haclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadMigrationDefinitionReadOnlyAndRedactedErrors(t *testing.T) {
	for _, kind := range []string{"automation", "scene", "script"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet ||
						r.URL.Path != "/api/config/"+kind+"/config/object-id" ||
						r.Header.Get("Authorization") != "Bearer private-token" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					_, _ = w.Write([]byte(`{"id":"object-id","name":"value"}`))
				}),
			)
			defer server.Close()
			got, err := NewClient(
				server.URL,
			).ReadMigrationDefinition(context.Background(), "private-token", kind, "object-id")
			if err != nil || !strings.Contains(string(got), "object-id") {
				t.Fatalf("definition = %s, %v", got, err)
			}
		})
	}
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("private-token"))
		}),
	)
	defer server.Close()
	_, err := NewClient(
		server.URL,
	).ReadMigrationDefinition(context.Background(), "private-token", "automation", "object-id")
	if err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestReadMigrationDefinitionRejectsInvalidIncompleteAndOversizedData(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[]`, `invalid`, `{"data":"` + strings.Repeat("x", maxMigrationDefinitionBytes) + `"}`} {
		server := httptest.NewServer(
			http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) },
			),
		)
		_, err := NewClient(
			server.URL,
		).ReadMigrationDefinition(context.Background(), "token", "script", "id")
		server.Close()
		if err == nil {
			t.Fatal("invalid or oversized definition accepted")
		}
	}
	if _, err := NewClient("http://example.test").ReadMigrationDefinition(context.Background(), "token", "integration", "id"); err == nil {
		t.Fatal("unsupported kind accepted")
	}
}

func TestReadMigrationIntegration(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(
				[]byte(
					`[{"entry_id":"entry-1","domain":"todo","title":"Todo","state":"loaded","source":"user"}]`,
				),
			)
		}),
	)
	defer server.Close()
	observation, err := NewClient(
		server.URL,
	).ReadMigrationIntegration(context.Background(), "token", " entry-1 ")
	if err != nil || observation.ID != "entry-1" || observation.Kind != "integration" {
		t.Fatalf("ReadMigrationIntegration() = %#v, %v", observation, err)
	}
}

func TestReadMigrationIntegrationRejectsMissingAndUnauthorized(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		}),
	)
	defer server.Close()
	client := NewClient(server.URL)
	if _, err := client.ReadMigrationIntegration(context.Background(), "token", "missing"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing object error = %v", err)
	}
	if _, err := client.ReadMigrationIntegration(context.Background(), "wrong", "missing"); err == nil {
		t.Fatal("unauthorized read succeeded")
	}
	if _, err := client.ReadMigrationIntegration(context.Background(), "token", " "); err == nil {
		t.Fatal("empty identity succeeded")
	}
}

func TestReadMigrationIntegrationRejectsAmbiguousAndIncompleteEntries(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate":      `[{"entry_id":"entry","domain":"a"},{"entry_id":"entry","domain":"b"}]`,
		"missing domain": `[{"entry_id":"entry"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(
					func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) },
				),
			)
			defer server.Close()
			if _, err := NewClient(server.URL).ReadMigrationIntegration(context.Background(), "token", "entry"); err == nil {
				t.Fatal("ambiguous or incomplete integration accepted")
			}
		})
	}
}

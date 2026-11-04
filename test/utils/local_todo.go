package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// HAEntityState is the limited state projection used by recovery fixtures.
// It contains no credentials and is deliberately separate from the operator's
// production Home Assistant client.
type HAEntityState struct {
	EntityID   string         `json:"entity_id"`
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes"`
}

// ListHAStates reads entities from an authenticated test fixture. Errors avoid
// response bodies because they can contain user-originated entity attributes.
func ListHAStates(ctx context.Context, client *http.Client, baseURL, token string) ([]HAEntityState, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/api/states", nil)
	if err != nil {
		return nil, fmt.Errorf("create state list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list fixture states: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fixture state list rejected: HTTP %d", response.StatusCode)
	}
	var states []HAEntityState
	if err := json.NewDecoder(response.Body).Decode(&states); err != nil {
		return nil, fmt.Errorf("decode fixture states: %w", err)
	}
	return states, nil
}

// AddLocalTodoItem inserts a fixture-only control item into a local To-do
// entity. The item is sent in the request body, never in an error or URL.
func AddLocalTodoItem(ctx context.Context, client *http.Client, baseURL, token, entityID, item string) error {
	if strings.TrimSpace(entityID) == "" || strings.TrimSpace(item) == "" {
		return fmt.Errorf("local To-do entity and item are required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	payload, err := json.Marshal(map[string]string{"entity_id": entityID, "item": item})
	if err != nil {
		return fmt.Errorf("encode local To-do item: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/api/services/todo/add_item", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create local To-do request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("add local To-do item: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("local To-do item rejected: HTTP %d", response.StatusCode)
	}
	return nil
}

// LocalTodoEntity returns the entity exposed by a Local To-do config entry.
// The fixture title is intentionally unique, so title matching cannot select a
// user's list. Entity IDs stay inside the test process and are not logged.
func LocalTodoEntity(states []HAEntityState, title string) (string, bool) {
	for _, state := range states {
		if !strings.HasPrefix(state.EntityID, "todo.") {
			continue
		}
		friendlyName, _ := state.Attributes["friendly_name"].(string)
		if friendlyName == title {
			return state.EntityID, true
		}
	}
	return "", false
}

package haclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrLovelaceYAMLMode indicates the default Lovelace dashboard is managed via YAML
// rather than the UI ("storage" mode). Home Assistant does not expose an API to
// register a Lovelace resource in that mode (the same limitation HACS itself has).
// Callers should treat this as an informational edge case, not an installation
// failure: the plugin file itself was still placed correctly.
var ErrLovelaceYAMLMode = errors.New(
	"lovelace dashboard is in YAML mode; resource registration is not available via API")

// LovelaceResource represents a single registered Lovelace resource (a JS module,
// legacy JS, HTML import, or CSS file added to the frontend).
type LovelaceResource struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "module", "js", "html", or "css"
	URL  string `json:"url"`
}

// ReloadThemes triggers a hot-reload of Home Assistant's frontend themes
// (service frontend.reload_themes). Used to activate a newly installed "theme"
// category community repository without a pod restart.
func (c *Client) ReloadThemes(ctx context.Context, token string) error {
	return c.postServiceWithToken(
		ctx,
		"/api/services/frontend/reload_themes",
		token,
		"failed to reload themes",
	)
}

// ReloadPythonScripts triggers a hot-reload of Home Assistant's python_script
// integration (service python_script.reload). Used to activate a newly installed
// "python_script" category community repository without a pod restart.
func (c *Client) ReloadPythonScripts(ctx context.Context, token string) error {
	return c.postServiceWithToken(
		ctx,
		"/api/services/python_script/reload",
		token,
		"failed to reload python scripts",
	)
}

// ReloadCustomTemplates triggers a hot-reload of Home Assistant's custom Jinja
// templates (service homeassistant.reload_custom_templates). Used to activate a
// newly installed "template" category community repository without a pod restart.
func (c *Client) ReloadCustomTemplates(ctx context.Context, token string) error {
	return c.postServiceWithToken(
		ctx,
		"/api/services/homeassistant/reload_custom_templates",
		token,
		"failed to reload custom templates",
	)
}

// sendWebSocketCommandWithCode is like SendWebSocketCommand but also returns HA's
// machine-readable error code (WSError.Code) on failure, so callers can distinguish
// specific error conditions (e.g. YAML-mode dashboards) from generic failures.
func (c *Client) sendWebSocketCommandWithCode(
	ctx context.Context,
	token string,
	msgType string,
	data map[string]interface{},
) (json.RawMessage, string, error) {
	if c.writeGuard == nil || readOnlyWebSocketCommand(msgType) {
		return c.sendUnguardedWebSocketCommand(ctx, token, msgType, data)
	}
	var result json.RawMessage
	var code string
	err := c.writeGuard(ctx, func(writeCtx context.Context) error {
		var err error
		result, code, err = c.sendUnguardedWebSocketCommand(writeCtx, token, msgType, data)
		return err
	})
	return result, code, err
}

func (c *Client) sendUnguardedWebSocketCommand(
	ctx context.Context, token, msgType string, data map[string]interface{},
) (json.RawMessage, string, error) {
	conn, err := c.wsAuthConnect(ctx, token)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = conn.Close() }()

	msg := make(map[string]interface{})
	for k, v := range data {
		msg[k] = v
	}
	msg["id"] = 1
	msg["type"] = msgType

	if err := conn.WriteJSON(msg); err != nil {
		return nil, "", &Error{Type: ErrorTypeHTTP, Message: "failed to send websocket command", Err: err}
	}

	var resp WebSocketResponse
	if err := conn.ReadJSON(&resp); err != nil {
		return nil, "", &Error{Type: ErrorTypeHTTP, Message: "failed to read websocket response", Err: err}
	}

	if !resp.Success {
		code := ""
		errMsg := "unknown error"
		if resp.Error != nil {
			code = resp.Error.Code
			if resp.Error.Message != "" {
				errMsg = resp.Error.Message
			}
		}
		return nil, code, &Error{
			Type:    ErrorTypeHTTP,
			Message: fmt.Sprintf("websocket command %q failed: %s", msgType, errMsg),
		}
	}

	return resp.Result, "", nil
}

// ListLovelaceResources returns all registered Lovelace resources for the default
// (storage-mode) dashboard. Returns ErrLovelaceYAMLMode if the dashboard is managed
// via YAML instead.
func (c *Client) ListLovelaceResources(ctx context.Context, token string) ([]LovelaceResource, error) {
	result, code, err := c.sendWebSocketCommandWithCode(ctx, token, "lovelace/resources", nil)
	if err != nil {
		if code == "not_supported" || code == "unknown_command" {
			return nil, fmt.Errorf("%w: %v", ErrLovelaceYAMLMode, err)
		}
		return nil, err
	}

	var resources []LovelaceResource
	if err := json.Unmarshal(result, &resources); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to parse lovelace resources", Err: err}
	}
	return resources, nil
}

// RegisterLovelaceResource registers a Lovelace resource (e.g. a HACS "plugin"
// category JS module) with the default dashboard, if not already registered.
// Idempotent: a no-op if a resource with the same URL already exists.
// Returns ErrLovelaceYAMLMode when the dashboard is YAML-managed — the caller
// should treat this as informational, not a failure.
func (c *Client) RegisterLovelaceResource(ctx context.Context, token, resourceURL, resType string) error {
	existing, err := c.ListLovelaceResources(ctx, token)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.URL == resourceURL {
			return nil
		}
	}

	_, code, err := c.sendWebSocketCommandWithCode(ctx, token, "lovelace/resources/create", map[string]interface{}{
		"res_type": resType,
		"url":      resourceURL,
	})
	if err != nil {
		if code == "not_supported" || code == "unknown_command" {
			return fmt.Errorf("%w: %v", ErrLovelaceYAMLMode, err)
		}
		return err
	}
	return nil
}

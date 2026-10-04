package haclient

import (
	"context"
	"encoding/json"
)

// ListDashboards returns all Lovelace dashboards, including storage-mode dashboards.
func (c *Client) ListDashboards(ctx context.Context, token string) ([]Dashboard, error) {
	result, err := c.SendWebSocketCommand(ctx, token, "lovelace/dashboards/list", nil)
	if err != nil {
		return nil, err
	}

	var dashboards []Dashboard
	if err := json.Unmarshal(result, &dashboards); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to parse dashboards", Err: err}
	}
	return dashboards, nil
}

// CreateDashboard creates a storage-mode Lovelace dashboard.
func (c *Client) CreateDashboard(ctx context.Context, token string, dashboard Dashboard) (*Dashboard, error) {
	result, err := c.SendWebSocketCommand(ctx, token, "lovelace/dashboards/create", dashboard.createCommandData())
	if err != nil {
		return nil, err
	}
	return parseDashboard(result)
}

// UpdateDashboard updates the metadata of an existing storage-mode Lovelace dashboard.
func (c *Client) UpdateDashboard(
	ctx context.Context, token, dashboardID string, dashboard Dashboard,
) (*Dashboard, error) {
	data := dashboard.updateCommandData()
	data["dashboard_id"] = dashboardID
	result, err := c.SendWebSocketCommand(ctx, token, "lovelace/dashboards/update", data)
	if err != nil {
		return nil, err
	}
	return parseDashboard(result)
}

// GetDashboardConfig returns the Lovelace configuration for a dashboard.
func (c *Client) GetDashboardConfig(ctx context.Context, token, urlPath string) (json.RawMessage, error) {
	return c.SendWebSocketCommand(ctx, token, "lovelace/config", map[string]interface{}{"url_path": urlPath})
}

// SaveDashboardConfig persists a Lovelace configuration for a storage-mode dashboard.
func (c *Client) SaveDashboardConfig(ctx context.Context, token, urlPath string, config json.RawMessage) error {
	_, err := c.SendWebSocketCommand(ctx, token, "lovelace/config/save", map[string]interface{}{
		"url_path": urlPath,
		"config":   config,
	})
	return err
}

// DeleteDashboard deletes a Lovelace dashboard. A missing dashboard is handled by Home Assistant.
func (c *Client) DeleteDashboard(ctx context.Context, token, dashboardID string) error {
	_, err := c.SendWebSocketCommand(ctx, token, "lovelace/dashboards/delete", map[string]interface{}{
		"dashboard_id": dashboardID,
	})
	return err
}

func (d Dashboard) createCommandData() map[string]interface{} {
	data := map[string]interface{}{
		"url_path":        d.URLPath,
		"title":           d.Title,
		"show_in_sidebar": d.ShowInSidebar,
		"require_admin":   d.RequireAdmin,
	}
	if d.Icon != "" {
		data["icon"] = d.Icon
	}
	if d.AllowSingleWord {
		data["allow_single_word"] = true
	}
	return data
}

func (d Dashboard) updateCommandData() map[string]interface{} {
	data := d.createCommandData()
	delete(data, "url_path")
	if d.Icon == "" {
		// Home Assistant removes the stored icon when update receives null.
		data["icon"] = nil
	}
	return data
}

func parseDashboard(result json.RawMessage) (*Dashboard, error) {
	var dashboard Dashboard
	if err := json.Unmarshal(result, &dashboard); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to parse dashboard", Err: err}
	}
	return &dashboard, nil
}

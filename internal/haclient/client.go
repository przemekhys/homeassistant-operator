package haclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultTimeout  = 30 * time.Second
	userAgent       = "homeassistant-operator/1.0"
	flowTypeSuccess = "create_entry"
)

// Client is a client for Home Assistant API
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Home Assistant API client
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
}

// WithTimeout sets a custom timeout for the HTTP client
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	c.httpClient.Timeout = timeout
	return c
}

// CheckHealth checks if Home Assistant is responding
// Returns nil if healthy, ErrorTypeNotReady if not ready, ErrorTypeBanned if IP is banned.
// Note: 401 Unauthorized is considered healthy (HA is running but needs onboarding)
func (c *Client) CheckHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/", nil)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Type: ErrorTypeNotReady, Message: "Home Assistant not responding", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// 200 OK = healthy with auth, 401 Unauthorized = healthy but needs onboarding
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
		return nil
	}

	// 403 Forbidden = operator IP has been banned by HA's ip_bans.yaml.
	// Trigger self-unban so the operator can recover automatically.
	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := fmt.Sprintf("operator IP banned by HA (HTTP 403): %s",
			strings.TrimSpace(string(body)))
		return &Error{
			Type:       ErrorTypeBanned,
			Message:    msg,
			StatusCode: resp.StatusCode,
		}
	}

	// 429 Too Many Requests = HA rate-limiting, not a ban.
	// Treat as transient not-ready so the caller backs off without
	// consuming the selfUnbanMaxCount budget.
	if resp.StatusCode == http.StatusTooManyRequests {
		return &Error{
			Type:       ErrorTypeNotReady,
			Message:    "Home Assistant rate-limiting requests (HTTP 429)",
			StatusCode: resp.StatusCode,
		}
	}

	return &Error{
		Type:       ErrorTypeNotReady,
		Message:    "Home Assistant not ready",
		StatusCode: resp.StatusCode,
	}
}

// CheckAPIReady verifies that Home Assistant API routes are fully registered.
// During startup, HA's HTTP server responds on /api/ (health check passes) before
// all components finish registering their routes. This method hits /api/config
// without auth — once routes are loaded it returns 401, during startup it returns 404.
// Returns nil if API is fully ready, ErrorTypeNotReady if routes not loaded yet.
func (c *Client) CheckAPIReady(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/config", nil)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Type: ErrorTypeNotReady, Message: "API readiness check failed", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// 401 = routes registered, API fully loaded (expected without auth)
	// 200 = API responded with config (unexpected without auth, but API is ready)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK {
		return nil
	}

	// 404 = routes not yet registered (startup race)
	if resp.StatusCode == http.StatusNotFound {
		return &Error{
			Type:    ErrorTypeNotReady,
			Message: "API routes not fully registered yet (404 from /api/config)",
		}
	}

	return &Error{
		Type:       ErrorTypeNotReady,
		Message:    fmt.Sprintf("unexpected status %d from /api/config readiness check", resp.StatusCode),
		StatusCode: resp.StatusCode,
	}
}

// CheckOnboardingStatus checks if onboarding is needed.
// Returns nil if onboarding needed, ErrorTypeOnboardingDone if already done.
// HA returns 200 + JSON array of steps when onboarding is pending,
// and 404 when onboarding is fully complete (endpoint is not registered).
//
// Caveat: during HA startup, /api/onboarding may briefly return 404 before
// the onboarding component registers its views. Callers should not trust a
// single OnboardingDone result — see reconcileBootstrap for confirmation logic.
func (c *Client) CheckOnboardingStatus(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/onboarding", nil)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to check onboarding", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 = onboarding fully complete (HA does not register the endpoint).
	// Include a snippet of the body so callers can distinguish a proper
	// backend 404 from an unexpected proxy/HTML 404.
	if resp.StatusCode == http.StatusNotFound {
		bodyBytes, _ := io.ReadAll(resp.Body)
		snippet := string(bodyBytes)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		return &Error{
			Type:    ErrorTypeOnboardingDone,
			Message: fmt.Sprintf("onboarding already completed (404 body: %s)", snippet),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("unexpected status code %d from /api/onboarding", resp.StatusCode),
			StatusCode: resp.StatusCode,
		}
	}

	// Parse the array of onboarding steps
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to read response", Err: err}
	}

	var steps []OnboardingStep
	if err := json.Unmarshal(body, &steps); err != nil {
		return &Error{Type: ErrorTypeInvalidResponse, Message: "failed to parse onboarding steps", Err: err}
	}

	// If the "user" step is done, onboarding was partially completed
	// (e.g. user created manually) — we can't re-run CreateUser
	for _, step := range steps {
		if step.Step == "user" && step.Done {
			return &Error{Type: ErrorTypeOnboardingDone, Message: "onboarding user step already completed"}
		}
	}

	// Onboarding needed
	return nil
}

// CreateUser creates initial admin user during onboarding
func (c *Client) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to marshal request", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/onboarding/users", bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// Connection errors (EOF, connection refused, etc.) indicate HA not ready yet
		// This allows the controller to retry with appropriate backoff
		return nil, &Error{Type: ErrorTypeNotReady, Message: "failed to create user: Home Assistant not ready", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		bodyStr := string(bodyBytes)

		// Check if user already exists - treat as idempotent success
		if strings.Contains(bodyStr, "User step already done") {
			return nil, &Error{
				Type:       ErrorTypeOnboardingDone,
				Message:    "user already created",
				StatusCode: resp.StatusCode,
			}
		}

		return nil, &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("failed to create user: %s", bodyStr),
			StatusCode: resp.StatusCode,
		}
	}

	var createResp CreateUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to decode response", Err: err}
	}

	if createResp.AuthCode == "" {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "auth_code not found in response"}
	}

	return &createResp, nil
}

// ExchangeAuthCode exchanges authorization code for access token
func (c *Client) ExchangeAuthCode(ctx context.Context, authCode, clientID string) (*TokenResponse, error) {
	formData := url.Values{}
	formData.Set("grant_type", "authorization_code")
	formData.Set("code", authCode)
	formData.Set("client_id", clientID)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/auth/token", strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// Connection errors indicate HA not responding - use NotReady type for retry
		return nil, &Error{
			Type:    ErrorTypeNotReady,
			Message: "failed to exchange auth code: Home Assistant not responding",
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, &Error{
			Type:       ErrorTypeAuth,
			Message:    fmt.Sprintf("failed to exchange auth code: %s", string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to decode token response", Err: err}
	}

	if tokenResp.AccessToken == "" {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "access_token not found in response"}
	}

	return &tokenResp, nil
}

// LoginWithCredentials authenticates with username/password via HA's login flow.
// This is used when onboarding is already done and we need an access token.
// Flow: POST /auth/login_flow → POST /auth/login_flow/{flow_id} → POST /auth/token
func (c *Client) LoginWithCredentials(
	ctx context.Context, username, password string,
) (*TokenResponse, error) {
	clientID := c.baseURL + "/"

	// Step 1: Create login flow
	// HA expects handler as a list: ["homeassistant", null]
	flowReqBody, _ := json.Marshal(map[string]interface{}{
		"client_id":    clientID,
		"handler":      []interface{}{"homeassistant", nil},
		"redirect_uri": clientID,
	})
	flowReq, err := http.NewRequestWithContext(
		ctx, "POST", c.baseURL+"/auth/login_flow",
		bytes.NewReader(flowReqBody),
	)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to create login flow request", Err: err}
	}
	flowReq.Header.Set("Content-Type", "application/json")
	flowReq.Header.Set("User-Agent", userAgent)

	flowResp, err := c.httpClient.Do(flowReq)
	if err != nil {
		return nil, &Error{Type: ErrorTypeNotReady, Message: "failed to create login flow", Err: err}
	}
	defer func() { _ = flowResp.Body.Close() }()

	if flowResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(flowResp.Body)
		return nil, &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("login flow creation failed (HTTP %d): %s", flowResp.StatusCode, string(bodyBytes)),
			StatusCode: flowResp.StatusCode,
		}
	}

	var flowData struct {
		FlowID string `json:"flow_id"`
		Type   string `json:"type"`
	}
	if err := json.NewDecoder(flowResp.Body).Decode(&flowData); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to parse login flow response", Err: err}
	}

	// Step 2: Submit credentials
	credBody, _ := json.Marshal(map[string]string{
		"username":  username,
		"password":  password,
		"client_id": clientID,
	})
	credReq, err := http.NewRequestWithContext(
		ctx, "POST", c.baseURL+"/auth/login_flow/"+flowData.FlowID,
		bytes.NewReader(credBody),
	)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to create credential request", Err: err}
	}
	credReq.Header.Set("Content-Type", "application/json")
	credReq.Header.Set("User-Agent", userAgent)

	credResp, err := c.httpClient.Do(credReq)
	if err != nil {
		return nil, &Error{Type: ErrorTypeNotReady, Message: "failed to submit credentials", Err: err}
	}
	defer func() { _ = credResp.Body.Close() }()

	if credResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(credResp.Body)
		return nil, &Error{
			Type:       ErrorTypeAuth,
			Message:    fmt.Sprintf("login failed (HTTP %d): %s", credResp.StatusCode, string(bodyBytes)),
			StatusCode: credResp.StatusCode,
		}
	}

	var credData struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}
	if err := json.NewDecoder(credResp.Body).Decode(&credData); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to parse credential response", Err: err}
	}
	if credData.Type != flowTypeSuccess || credData.Result == "" {
		// type=form means HA rejected the credentials because no user exists yet —
		// onboarding was not completed. Use a distinct error type so the controller
		// can reset the onboarding confirmation window instead of giving up.
		errType := ErrorTypeAuth
		if credData.Type == "form" {
			errType = ErrorTypeLoginNoUser
		}
		return nil, &Error{
			Type:    errType,
			Message: fmt.Sprintf("login flow did not return auth code (type=%s)", credData.Type),
		}
	}

	// Step 3: Exchange auth code for access token
	return c.ExchangeAuthCode(ctx, credData.Result, clientID)
}

// wsAuthConnect opens a WebSocket connection to HA and performs the auth handshake.
// The caller is responsible for closing the returned connection.
func (c *Client) wsAuthConnect(ctx context.Context, token string) (*websocket.Conn, error) {
	wsURL := strings.Replace(c.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL += "/api/websocket"

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to connect to websocket", Err: err}
	}

	// Set deadlines from context or fallback to defaultTimeout
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultTimeout)
	}
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)

	// Read auth_required
	var authRequired map[string]interface{}
	if err := conn.ReadJSON(&authRequired); err != nil {
		_ = conn.Close()
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to read auth_required", Err: err}
	}
	if authRequired["type"] != "auth_required" {
		_ = conn.Close()
		return nil, &Error{
			Type:    ErrorTypeHTTP,
			Message: fmt.Sprintf("unexpected message type: %v", authRequired["type"]),
		}
	}

	// Send auth
	authMsg := map[string]interface{}{
		"type":         "auth",
		"access_token": token,
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		_ = conn.Close()
		return nil, &Error{Type: ErrorTypeAuth, Message: "failed to send auth message", Err: err}
	}

	// Read auth result
	var authResult map[string]interface{}
	if err := conn.ReadJSON(&authResult); err != nil {
		_ = conn.Close()
		return nil, &Error{Type: ErrorTypeAuth, Message: "failed to read auth result", Err: err}
	}
	if authResult["type"] != "auth_ok" {
		_ = conn.Close()
		return nil, &Error{
			Type:    ErrorTypeAuth,
			Message: fmt.Sprintf("auth failed: %v", authResult["message"]),
		}
	}

	return conn, nil
}

// SendWebSocketCommand sends a single command over WebSocket and returns the result.
// One-shot pattern: open → auth → send → receive → close.
func (c *Client) SendWebSocketCommand(
	ctx context.Context,
	token string,
	msgType string,
	data map[string]interface{},
) (json.RawMessage, error) {
	conn, err := c.wsAuthConnect(ctx, token)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	// Build command message.
	// id=1 is intentional: each call opens a fresh connection (one-shot pattern),
	// so there is no need for unique IDs. Revisit if connection reuse is added.
	msg := make(map[string]interface{})
	for k, v := range data {
		msg[k] = v
	}
	msg["id"] = 1
	msg["type"] = msgType

	if err := conn.WriteJSON(msg); err != nil {
		return nil, &Error{
			Type:    ErrorTypeHTTP,
			Message: "failed to send websocket command",
			Err:     err,
		}
	}

	var resp WebSocketResponse
	if err := conn.ReadJSON(&resp); err != nil {
		return nil, &Error{
			Type:    ErrorTypeHTTP,
			Message: "failed to read websocket response",
			Err:     err,
		}
	}

	if !resp.Success {
		errMsg := "unknown error"
		if resp.Error != nil && resp.Error.Message != "" {
			errMsg = resp.Error.Message
		}
		return nil, &Error{
			Type:    ErrorTypeHTTP,
			Message: fmt.Sprintf("websocket command %q failed: %s", msgType, errMsg),
		}
	}

	return resp.Result, nil
}

// CreateLongLivedToken creates a long-lived access token via WebSocket API.
// Home Assistant requires WebSocket for creating long-lived tokens.
func (c *Client) CreateLongLivedToken(
	ctx context.Context,
	accessToken string,
	req *LongLivedTokenRequest,
) (*LongLivedTokenResponse, error) {
	result, err := c.SendWebSocketCommand(ctx, accessToken, "auth/long_lived_access_token", map[string]interface{}{
		"client_name": req.ClientName,
		"lifespan":    req.Lifespan,
	})
	if err != nil {
		return nil, err
	}

	var token string
	if err := json.Unmarshal(result, &token); err != nil || token == "" {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "empty token received"}
	}

	return &LongLivedTokenResponse{Token: token}, nil
}

// GetBackupConfig retrieves the current backup configuration from HA via WebSocket.
func (c *Client) GetBackupConfig(
	ctx context.Context, token string,
) (*BackupConfig, error) {
	result, err := c.SendWebSocketCommand(
		ctx, token, "backup/config/info", nil,
	)
	if err != nil {
		return nil, err
	}

	var config BackupConfig
	if err := json.Unmarshal(result, &config); err != nil {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: "failed to parse backup config",
			Err:     err,
		}
	}
	return &config, nil
}

// ConfigureBackup updates the backup configuration in HA via WebSocket.
func (c *Client) ConfigureBackup(
	ctx context.Context, token string, req *BackupConfigRequest,
) error {
	data := make(map[string]interface{})
	if req.Schedule != nil {
		data["schedule"] = req.Schedule
	}
	if req.Retention != nil {
		data["retention"] = req.Retention
	}
	if req.CreateBackup != nil {
		data["create_backup"] = req.CreateBackup
	}
	_, err := c.SendWebSocketCommand(
		ctx, token, "backup/config/update", data,
	)
	return err
}

// SetCoreConfig configures location and core settings during onboarding
func (c *Client) SetCoreConfig(ctx context.Context, accessToken string, req *CoreConfigRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to marshal request", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/onboarding/core_config", bytes.NewReader(body))
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to set core config", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("failed to set core config (HTTP %d): %s", resp.StatusCode, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	return nil
}

// SetAnalytics configures analytics preferences during onboarding
func (c *Client) SetAnalytics(ctx context.Context, accessToken string, enabled bool) error {
	var req AnalyticsRequest
	if enabled {
		req.Preferences = &AnalyticsPreferences{
			Base:        true,
			Diagnostics: true,
			Usage:       true,
			Statistics:  true,
		}
	}
	// If disabled, send empty preferences (or nil)

	body, err := json.Marshal(req)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to marshal request", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/onboarding/analytics", bytes.NewReader(body))
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to set analytics", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("failed to set analytics (HTTP %d): %s", resp.StatusCode, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	return nil
}

// CompleteIntegrationStep marks the integration onboarding step as done.
// This is the 4th and final onboarding step required by Home Assistant.
// Without it, non-admin users are blocked from accessing the websocket API.
func (c *Client) CompleteIntegrationStep(ctx context.Context, accessToken string) error {
	httpReq, err := http.NewRequestWithContext(
		ctx, "POST", c.baseURL+"/api/onboarding/integration",
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to complete integration step", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("failed to complete integration step (HTTP %d): %s", resp.StatusCode, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	return nil
}

// BootstrapOptions contains optional configuration for bootstrap
type BootstrapOptions struct {
	CreateLongLivedToken bool
	CoreConfig           *CoreConfigRequest
	EnableAnalytics      bool
}

// PerformBootstrap performs complete bootstrap flow:
// 1. Check health
// 2. Check onboarding status
// 3. Create user
// 4. Exchange auth code for token
// 5. Set core config (location) if provided
// 6. Set analytics preferences
// 7. Complete integration step (marks onboarding as fully done)
// 8. Create long-lived token (if requested)
// Returns the long-lived token or empty string if not created
func (c *Client) PerformBootstrap(
	ctx context.Context,
	username, password, ownerName, language string,
	opts *BootstrapOptions,
) (string, error) {
	if opts == nil {
		opts = &BootstrapOptions{}
	}

	// 1. Check health
	if err := c.CheckHealth(ctx); err != nil {
		return "", err
	}

	// 2. Check onboarding status
	if err := c.CheckOnboardingStatus(ctx); err != nil {
		if IsOnboardingDone(err) {
			// Already done - this is not an error
			return "", &Error{Type: ErrorTypeOnboardingDone, Message: "onboarding already completed"}
		}
		return "", err
	}

	// 3. Create user
	clientID := c.baseURL + "/"
	userResp, err := c.CreateUser(ctx, &CreateUserRequest{
		ClientID: clientID,
		Name:     ownerName,
		Username: username,
		Password: password,
		Language: language,
	})
	if err != nil {
		return "", err
	}

	// 4. Exchange auth code (needed for subsequent steps)
	tokenResp, err := c.ExchangeAuthCode(ctx, userResp.AuthCode, clientID)
	if err != nil {
		return "", err
	}

	// 5. Set core config (location) if provided - requires auth token
	if opts.CoreConfig != nil {
		// Ignore errors - core config is optional and failure doesn't block bootstrap
		_ = c.SetCoreConfig(ctx, tokenResp.AccessToken, opts.CoreConfig)
	}

	// 6. Set analytics preferences - requires auth token
	// Ignore errors - analytics config is optional and failure doesn't block bootstrap
	_ = c.SetAnalytics(ctx, tokenResp.AccessToken, opts.EnableAnalytics)

	// 7. Complete integration step - marks onboarding as fully done
	// Unlike core_config/analytics, this is NOT optional: without it
	// non-admin users are blocked from accessing the websocket API.
	if err := c.CompleteIntegrationStep(ctx, tokenResp.AccessToken); err != nil {
		return "", err
	}

	// 8. Create long-lived token if requested
	if !opts.CreateLongLivedToken {
		return "", nil
	}

	longLivedResp, err := c.CreateLongLivedToken(ctx, tokenResp.AccessToken, &LongLivedTokenRequest{
		ClientName: "kubernetes-operator",
		Lifespan:   3650, // 10 years
	})
	if err != nil {
		return "", err
	}

	return longLivedResp.Token, nil
}

// CheckConfig validates the current Home Assistant configuration
// Returns nil if config is valid, error if invalid
// Requires authenticated API call with long-lived token
func (c *Client) CheckConfig(ctx context.Context, token string) error {
	httpReq, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.baseURL+"/api/services/homeassistant/check_config",
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to check config", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("config check failed: %s", string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	// Parse response - Home Assistant can return either array or object
	var rawResponse json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rawResponse); err != nil {
		return &Error{Type: ErrorTypeInvalidResponse, Message: "failed to decode response", Err: err}
	}

	// Try to parse as object first (expected format for errors)
	var resultObj map[string]interface{}
	if err := json.Unmarshal(rawResponse, &resultObj); err == nil {
		// It's an object - check for various error formats

		// Check for "errors" field (array)
		if errors, ok := resultObj["errors"].([]interface{}); ok && len(errors) > 0 {
			return &Error{
				Type:    ErrorTypeHTTP,
				Message: fmt.Sprintf("config validation errors: %v", errors),
			}
		}

		// Check for "errors" field (string)
		if errorStr, ok := resultObj["errors"].(string); ok && errorStr != "" {
			return &Error{
				Type:    ErrorTypeHTTP,
				Message: fmt.Sprintf("config validation error: %s", errorStr),
			}
		}

		// Check for "error" field (string)
		if errorStr, ok := resultObj["error"].(string); ok && errorStr != "" {
			return &Error{
				Type:    ErrorTypeHTTP,
				Message: fmt.Sprintf("config validation error: %s", errorStr),
			}
		}

		// Check for "message" field (string)
		if message, ok := resultObj["message"].(string); ok && message != "" {
			return &Error{
				Type:    ErrorTypeHTTP,
				Message: fmt.Sprintf("config validation message: %s", message),
			}
		}

		return nil
	}

	// Try to parse as array (valid response from service call)
	var resultArray []interface{}
	if err := json.Unmarshal(rawResponse, &resultArray); err == nil {
		// It's an array - treat as success
		return nil
	}

	// If neither worked, return error
	return &Error{
		Type:    ErrorTypeInvalidResponse,
		Message: "unexpected response format (neither array nor object)",
	}
}

// postServiceWithToken sends a POST request to a Home Assistant service
// endpoint with token authentication
func (c *Client) postServiceWithToken(
	ctx context.Context,
	endpoint string,
	token string,
	errorMsg string,
) error {
	httpReq, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.baseURL+endpoint,
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		return &Error{
			Type:    ErrorTypeHTTP,
			Message: "failed to create request",
			Err:     err,
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{
			Type:    ErrorTypeHTTP,
			Message: errorMsg,
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("%s: %s", errorMsg, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	return nil
}

// ReloadCoreConfig triggers a hot-reload of Home Assistant core
// configuration. Returns nil if reload successful, error if failed.
// Requires authenticated API call with long-lived token.
func (c *Client) ReloadCoreConfig(
	ctx context.Context,
	token string,
) error {
	return c.postServiceWithToken(
		ctx,
		"/api/services/homeassistant/reload_core_config",
		token,
		"failed to reload config",
	)
}

// ReloadAutomations triggers a hot-reload of Home Assistant automations.
// Returns nil if reload successful, error if failed.
// Requires authenticated API call with long-lived token.
func (c *Client) ReloadAutomations(
	ctx context.Context,
	token string,
) error {
	return c.postServiceWithToken(
		ctx,
		"/api/services/automation/reload",
		token,
		"failed to reload automations",
	)
}

// ReloadScenes triggers a hot-reload of Home Assistant scenes.
// Returns nil if reload successful, error if failed.
// Requires authenticated API call with long-lived token.
func (c *Client) ReloadScenes(
	ctx context.Context,
	token string,
) error {
	return c.postServiceWithToken(
		ctx,
		"/api/services/scene/reload",
		token,
		"failed to reload scenes",
	)
}

// ReloadScripts triggers a hot-reload of Home Assistant scripts.
// Returns nil if reload successful, error if failed.
// Requires authenticated API call with long-lived token.
func (c *Client) ReloadScripts(
	ctx context.Context,
	token string,
) error {
	return c.postServiceWithToken(
		ctx,
		"/api/services/script/reload",
		token,
		"failed to reload scripts",
	)
}

// GetConfig returns Home Assistant configuration including loaded components.
// This is useful for checking if specific integrations are loaded.
// Requires authenticated API call with long-lived token.
func (c *Client) GetConfig(ctx context.Context, token string) (*ConfigResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/config", nil)
	if err != nil {
		return nil, &Error{
			Type:    ErrorTypeHTTP,
			Message: "failed to create config request",
			Err:     err,
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &Error{
			Type:    ErrorTypeNotReady,
			Message: "failed to get config",
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("failed to get config: %s", string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	var config ConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: "failed to decode config response",
			Err:     err,
		}
	}

	return &config, nil
}

// postConfig sends a POST request to a HA config endpoint with JSON body.
// Used for creating/updating automation, scene and script configs via REST API.
// HA uses POST (not PUT) for /api/config/{type}/config/{id} endpoints.
func (c *Client) postConfig(ctx context.Context, token, path string, data map[string]interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to marshal request", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{Type: ErrorTypeNotReady, Message: fmt.Sprintf("failed to POST %s", path), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("HTTP: POST %s failed: %d: %s", path, resp.StatusCode, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	return nil
}

// deleteConfig sends a DELETE request to a HA config endpoint.
// 404 is treated as success (idempotent delete).
func (c *Client) deleteConfig(ctx context.Context, token, path string) error {
	httpReq, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+path, nil)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{Type: ErrorTypeNotReady, Message: fmt.Sprintf("failed to DELETE %s", path), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 = already gone, treat as success (idempotent)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("DELETE %s failed: %s", path, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	return nil
}

// configPath builds a safe REST API path for a config entry, percent-encoding the id.
func configPath(resourceType, id string) string {
	return "/api/config/" + resourceType + "/config/" + url.PathEscape(id)
}

// PutAutomation creates or updates an automation via HA REST API.
// HA writes the result to automations.yaml on the PVC (writable).
// Idempotent: safe to call on every reconcile.
func (c *Client) PutAutomation(ctx context.Context, token, id string, data map[string]interface{}) error {
	return c.postConfig(ctx, token, configPath("automation", id), data)
}

// DeleteAutomation removes an automation via HA REST API.
// Idempotent: returns nil if automation does not exist (404).
func (c *Client) DeleteAutomation(ctx context.Context, token, id string) error {
	return c.deleteConfig(ctx, token, configPath("automation", id))
}

// DisableAutomation disables an automation via HA REST API.
func (c *Client) DisableAutomation(ctx context.Context, token, id string) error {
	return c.postConfig(ctx, token, configPath("automation", id)+"/disable", map[string]interface{}{})
}

// PutScene creates or updates a scene via HA REST API.
// Idempotent: safe to call on every reconcile.
func (c *Client) PutScene(ctx context.Context, token, id string, data map[string]interface{}) error {
	return c.postConfig(ctx, token, configPath("scene", id), data)
}

// DeleteScene removes a scene via HA REST API.
// Idempotent: returns nil if scene does not exist (404).
func (c *Client) DeleteScene(ctx context.Context, token, id string) error {
	return c.deleteConfig(ctx, token, configPath("scene", id))
}

// PutScript creates or updates a script via HA REST API.
// Idempotent: safe to call on every reconcile.
func (c *Client) PutScript(ctx context.Context, token, id string, data map[string]interface{}) error {
	return c.postConfig(ctx, token, configPath("script", id), data)
}

// DeleteScript removes a script via HA REST API.
// Idempotent: returns nil if script does not exist (404).
func (c *Client) DeleteScript(ctx context.Context, token, id string) error {
	return c.deleteConfig(ctx, token, configPath("script", id))
}

// getJSON performs an authenticated GET request and decodes the JSON response into target.
func (c *Client) getJSON(ctx context.Context, token, path string, target interface{}) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &Error{Type: ErrorTypeNotReady, Message: fmt.Sprintf("GET %s failed", path), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("GET %s: %d: %s", path, resp.StatusCode, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return &Error{Type: ErrorTypeInvalidResponse, Message: "failed to decode response", Err: err}
	}
	return nil
}

// ListConfigEntries returns all config entries from Home Assistant.
func (c *Client) ListConfigEntries(ctx context.Context, token string) ([]ConfigEntry, error) {
	var entries []ConfigEntry
	if err := c.getJSON(ctx, token, "/api/config/config_entries/entry", &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// IsIntegrationConfigured checks if a config entry exists for the given domain.
func (c *Client) IsIntegrationConfigured(ctx context.Context, token, domain string) (bool, error) {
	entries, err := c.ListConfigEntries(ctx, token)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Domain == domain {
			return true, nil
		}
	}
	return false, nil
}

// StartConfigFlow initiates a config entry flow for the given integration domain.
func (c *Client) StartConfigFlow(ctx context.Context, token, domain string) (*FlowResponse, error) {
	data := map[string]interface{}{
		"handler": domain,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to marshal request", Err: err}
	}

	flowURL := c.baseURL + "/api/config/config_entries/flow"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", flowURL, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &Error{Type: ErrorTypeNotReady, Message: "failed to start config flow", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("start config flow failed: %s", string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	var flowResp FlowResponse
	if err := json.NewDecoder(resp.Body).Decode(&flowResp); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to decode flow response", Err: err}
	}
	return &flowResp, nil
}

// SubmitConfigFlow submits data to a config entry flow step.
func (c *Client) SubmitConfigFlow(
	ctx context.Context, token, flowID string, data map[string]interface{},
) (*FlowResponse, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to marshal request", Err: err}
	}

	path := "/api/config/config_entries/flow/" + url.PathEscape(flowID)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Type: ErrorTypeHTTP, Message: "failed to create request", Err: err}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &Error{Type: ErrorTypeNotReady, Message: "failed to submit config flow", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, &Error{
			Type:       ErrorTypeHTTP,
			Message:    fmt.Sprintf("submit config flow failed: %s", string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	var flowResp FlowResponse
	if err := json.NewDecoder(resp.Body).Decode(&flowResp); err != nil {
		return nil, &Error{Type: ErrorTypeInvalidResponse, Message: "failed to decode flow response", Err: err}
	}
	return &flowResp, nil
}

// SubmitConfigFlowUntilDone submits config flow steps until the flow reaches
// flowTypeSuccess (success) or "abort" (failure), or until stepsData is exhausted.
// Each element in stepsData is submitted as one form step in sequence.
// Returns the final FlowResponse when type becomes flowTypeSuccess.
func (c *Client) SubmitConfigFlowUntilDone(
	ctx context.Context, token, flowID string, stepsData []map[string]interface{},
) (*FlowResponse, error) {
	for i, stepData := range stepsData {
		resp, err := c.SubmitConfigFlow(ctx, token, flowID, stepData)
		if err != nil {
			return nil, err
		}
		switch resp.Type {
		case flowTypeSuccess:
			return resp, nil
		case "abort":
			return nil, &Error{
				Type:    ErrorTypeHTTP,
				Message: fmt.Sprintf("config flow aborted at step %d", i+1),
			}
		}
		// type == "form" or similar — continue to next step
	}
	return nil, &Error{
		Type:    ErrorTypeHTTP,
		Message: fmt.Sprintf("config flow did not complete after %d step(s)", len(stepsData)),
	}
}

// ParseConfigEntryResult parses the Result field of a FlowResponse after a successful create_entry.
// Returns ConfigEntryResult with the entry_id, domain and title of the created config entry.
func ParseConfigEntryResult(resp *FlowResponse) (*ConfigEntryResult, error) {
	if resp.Type != flowTypeSuccess {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: fmt.Sprintf("expected flow type create_entry, got %s", resp.Type),
		}
	}
	if resp.Result == nil {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: "flow result is empty",
		}
	}
	var result ConfigEntryResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: "failed to parse config entry result",
			Err:     err,
		}
	}
	if result.EntryID == "" {
		return nil, &Error{
			Type:    ErrorTypeInvalidResponse,
			Message: "entry_id not found in flow result",
		}
	}
	return &result, nil
}

// RemoveConfigEntry deletes a config entry by its entry ID.
// Returns nil if the entry does not exist (404 treated as success).
func (c *Client) RemoveConfigEntry(ctx context.Context, token, entryID string) error {
	return c.deleteConfig(ctx, token, "/api/config/config_entries/entry/"+url.PathEscape(entryID))
}

// IsComponentLoaded checks if a specific component/integration is loaded in Home Assistant.
// Common components: "automation", "script", "scene", "homeassistant", "http", "mqtt", etc.
// Returns true if component is loaded, false otherwise.
// Requires authenticated API call with long-lived token.
func (c *Client) IsComponentLoaded(ctx context.Context, token, component string) (bool, error) {
	config, err := c.GetConfig(ctx, token)
	if err != nil {
		return false, err
	}

	for _, comp := range config.Components {
		if comp == component {
			return true, nil
		}
	}
	return false, nil
}

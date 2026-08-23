package haclient

import (
	"encoding/json"
	"time"
)

// OnboardingStep represents a step in the onboarding array returned by GET /api/onboarding.
type OnboardingStep struct {
	Step string `json:"step"`
	Done bool   `json:"done"`
}

// CreateUserRequest represents POST /api/onboarding/users request
type CreateUserRequest struct {
	ClientID string `json:"client_id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
	Language string `json:"language"`
}

// CreateUserResponse represents POST /api/onboarding/users response
type CreateUserResponse struct {
	AuthCode string `json:"auth_code"`
}

// TokenRequest represents form data for POST /auth/token
type TokenRequest struct {
	GrantType string
	Code      string
	ClientID  string
}

// TokenResponse represents POST /auth/token response
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// LongLivedTokenRequest represents POST /api/auth/long_lived_access_token request
type LongLivedTokenRequest struct {
	ClientName string `json:"client_name"`
	Lifespan   int    `json:"lifespan"` // days
}

// LongLivedTokenResponse is returned as plain string
type LongLivedTokenResponse struct {
	Token string
}

// CoreConfigRequest represents POST /api/onboarding/core_config request
type CoreConfigRequest struct {
	LocationName string  `json:"location_name,omitempty"`
	Latitude     float64 `json:"latitude,omitempty"`
	Longitude    float64 `json:"longitude,omitempty"`
	Elevation    int     `json:"elevation,omitempty"`
	UnitSystem   string  `json:"unit_system,omitempty"` // "metric" or "us_customary"
	Currency     string  `json:"currency,omitempty"`
	TimeZone     string  `json:"time_zone,omitempty"`
}

// AnalyticsRequest represents POST /api/onboarding/analytics request
type AnalyticsRequest struct {
	// Empty body for disabling analytics, or can include preferences field
	Preferences *AnalyticsPreferences `json:"preferences,omitempty"`
}

// AnalyticsPreferences defines analytics preferences
type AnalyticsPreferences struct {
	Base        bool `json:"base,omitempty"`
	Diagnostics bool `json:"diagnostics,omitempty"`
	Usage       bool `json:"usage,omitempty"`
	Statistics  bool `json:"statistics,omitempty"`
}

// ConfigResponse represents GET /api/config response
// Contains Home Assistant configuration including loaded components
type ConfigResponse struct {
	Components            []string `json:"components"`
	Version               string   `json:"version"`
	LocationName          string   `json:"location_name"`
	TimeZone              string   `json:"time_zone"`
	ConfigDir             string   `json:"config_dir"`
	WhitelistExternalDirs []string `json:"whitelist_external_dirs"`
}

// ConfigEntry represents a config entry from GET /api/config/config_entries/entry
type ConfigEntry struct {
	EntryID string `json:"entry_id"`
	Domain  string `json:"domain"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Source  string `json:"source"`
}

// FlowResponse represents a response from Config Entry Flow API
type FlowResponse struct {
	FlowID     string          `json:"flow_id"`
	Type       string          `json:"type"`
	Reason     string          `json:"reason,omitempty"`
	Title      string          `json:"title"`
	Result     json.RawMessage `json:"result"`
	Version    int             `json:"version"`
	DataSchema []FlowField     `json:"data_schema,omitempty"`
}

// FlowField represents a field in a config flow step
type FlowField struct {
	Name        string          `json:"name"`
	Required    bool            `json:"required"`
	Type        string          `json:"type"`
	Selector    map[string]any  `json:"selector,omitempty"`
	Description json.RawMessage `json:"description,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	ValueMin    *float64        `json:"value_min,omitempty"`
	ValueMax    *float64        `json:"value_max,omitempty"`
}

// ConfigEntryResult represents the result field from a successful create_entry flow response
type ConfigEntryResult struct {
	EntryID string `json:"entry_id"`
	Domain  string `json:"domain"`
	Title   string `json:"title"`
}

// WebSocketResponse represents a generic HA WebSocket command response.
type WebSocketResponse struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   *WSError        `json:"error,omitempty"`
}

// WSError represents the error object inside a failed WebSocket response.
type WSError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BackupSchedule represents the schedule section of HA backup config
type BackupSchedule struct {
	Recurrence string  `json:"recurrence"` // "daily", "mon", "tue", ..., "never"
	Time       *string `json:"time"`       // "HH:MM:SS" or null
}

// BackupRetention represents the retention section of HA backup config
type BackupRetention struct {
	Copies *int `json:"copies"` // null = unlimited
	Days   *int `json:"days"`   // null = unlimited
}

// BackupCreateConfig represents the create_backup section of HA backup config
type BackupCreateConfig struct {
	IncludeDatabase *bool    `json:"include_database,omitempty"`
	AgentIDs        []string `json:"agent_ids,omitempty"`
	Password        *string  `json:"password,omitempty"`
}

// BackupConfig represents the full HA backup configuration
type BackupConfig struct {
	Schedule     BackupSchedule     `json:"schedule"`
	Retention    BackupRetention    `json:"retention"`
	CreateBackup BackupCreateConfig `json:"create_backup"`
}

// BackupConfigRequest represents the request for backup/config/update
type BackupConfigRequest struct {
	Schedule     *BackupSchedule     `json:"schedule,omitempty"`
	Retention    *BackupRetention    `json:"retention,omitempty"`
	CreateBackup *BackupCreateConfig `json:"create_backup,omitempty"`
}

// HTTPConfig is the response of the "http/config" WebSocket command: the HA
// core http integration's own user-managed config store (distinct from the
// deprecated configuration.yaml http: block).
type HTTPConfig struct {
	Stable           HTTPConfigData  `json:"stable"`
	Pending          *HTTPConfigData `json:"pending"`
	RevertAt         *time.Time      `json:"revert_at"`
	ActiveConfigType string          `json:"active_config_type"`
}

// HTTPConfigData mirrors HA core's ConfData/HTTP_STORAGE_SCHEMA (the http
// integration's stored config). CreatedAt/Error/ErrorMessage are populated by
// HA itself and only ever read by the operator, never set: after a pending
// config fails its trial and auto-reverts, HA keeps Pending populated with
// Error/ErrorMessage rather than resetting it to nil ("kept... but never
// applied again") — Error != "" on Pending is the only correct signal that a
// rotation was rejected, never Pending == nil.
type HTTPConfigData struct {
	ServerPort             int      `json:"server_port,omitempty"`
	ServerHost             []string `json:"server_host,omitempty"`
	SSLCertificate         string   `json:"ssl_certificate,omitempty"`
	SSLKey                 string   `json:"ssl_key,omitempty"`
	SSLPeerCertificate     string   `json:"ssl_peer_certificate,omitempty"`
	SSLProfile             string   `json:"ssl_profile,omitempty"`
	UseXForwardedFor       bool     `json:"use_x_forwarded_for,omitempty"`
	TrustedProxies         []string `json:"trusted_proxies,omitempty"`
	CORSAllowedOrigins     []string `json:"cors_allowed_origins,omitempty"`
	LoginAttemptsThreshold int      `json:"login_attempts_threshold,omitempty"`
	IPBanEnabled           bool     `json:"ip_ban_enabled,omitempty"`
	UseXFrameOptions       bool     `json:"use_x_frame_options,omitempty"`
	CreatedAt              string   `json:"created_at,omitempty"`
	Error                  string   `json:"error,omitempty"`
	ErrorMessage           string   `json:"error_message,omitempty"`
}

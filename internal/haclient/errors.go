package haclient

import (
	"fmt"
	"net/http"
)

func ipBannedError() *Error {
	return &Error{Type: ErrorTypeBanned, Message: "operator IP banned by Home Assistant", StatusCode: http.StatusForbidden}
}

// ErrorType represents different error categories
type ErrorType string

const (
	ErrorTypeNotReady        ErrorType = "NotReady"        // HA not responding
	ErrorTypeOnboardingDone  ErrorType = "OnboardingDone"  // Already completed
	ErrorTypeHTTP            ErrorType = "HTTP"            // HTTP error
	ErrorTypeInvalidResponse ErrorType = "InvalidResponse" // Parse error
	ErrorTypeAuth            ErrorType = "Auth"            // Authentication error
	ErrorTypeLoginNoUser     ErrorType = "LoginNoUser"     // Login flow returned type=form (no user exists yet)
	ErrorTypeBanned          ErrorType = "Banned"          // Operator IP banned by HA ip_bans.yaml
)

// Error represents a Home Assistant API error
type Error struct {
	Type       ErrorType
	Message    string
	StatusCode int
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Type, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// IsNotReady returns true if error is NotReady type
func IsNotReady(err error) bool {
	if haErr, ok := err.(*Error); ok {
		return haErr.Type == ErrorTypeNotReady
	}
	return false
}

// IsOnboardingDone returns true if onboarding already completed
func IsOnboardingDone(err error) bool {
	if haErr, ok := err.(*Error); ok {
		return haErr.Type == ErrorTypeOnboardingDone
	}
	return false
}

// IsBanned returns true if the operator's IP has been banned by HA
func IsBanned(err error) bool {
	if haErr, ok := err.(*Error); ok {
		return haErr.Type == ErrorTypeBanned
	}
	return false
}

// IsLoginNoUser returns true if login failed because no user exists in HA yet
// (login flow returned type=form instead of type=create_entry).
// This indicates onboarding was not actually completed — HA startup may have
// returned a transient 404 from /api/onboarding before routes were registered.
func IsLoginNoUser(err error) bool {
	if haErr, ok := err.(*Error); ok {
		return haErr.Type == ErrorTypeLoginNoUser
	}
	return false
}

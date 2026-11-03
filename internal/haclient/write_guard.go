package haclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
)

// WriteGuard authorizes one synchronous mutation. The callback must be invoked
// at most once; an uncertain result must not authorize another attempt.
type WriteGuard func(context.Context, func(context.Context) error) error

// WithWriteGuard returns an independently configured client. Read-only commands
// remain available during a hold. Unknown commands are conservatively writes.
func (c *Client) WithWriteGuard(guard WriteGuard) *Client {
	copy := *c
	httpCopy := *c.httpClient
	copy.httpClient = &httpCopy
	copy.writeGuard = guard
	if guard != nil {
		transport := httpCopy.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		httpCopy.Transport = guardedTransport{next: transport, guard: guard}
		httpCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &copy
}

func readOnlyWebSocketCommand(command string) bool {
	switch command {
	case "config/floor_registry/list", "config/label_registry/list", "config/area_registry/list",
		"lovelace/dashboards/list", "lovelace/config", "lovelace/resources", "config/auth/list",
		"get_config", "http/config", "backup/config/info", "backup/agents/info", "backup/info", "backup/details":
		return true
	default:
		return false
	}
}

type guardedTransport struct {
	next  http.RoundTripper
	guard WriteGuard
}

func (t guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions {
		return t.next.RoundTrip(request)
	}
	var response *http.Response
	err := t.guard(request.Context(), func(ctx context.Context) error {
		var err error
		response, err = t.next.RoundTrip(request.Clone(ctx))
		if err != nil {
			return errors.New("home assistant mutation response unavailable")
		}
		// Consume the bounded response before confirming completion; a lost body
		// is uncertainty too. Never retain or log a raw error response.
		originalBody := response.Body
		defer func() { _ = originalBody.Close() }()
		const maxResponseBytes = 1024 * 1024
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		if readErr != nil || len(body) > maxResponseBytes || response.StatusCode >= 300 {
			return errors.New("home assistant mutation outcome is not confirmed")
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	return response, nil
}

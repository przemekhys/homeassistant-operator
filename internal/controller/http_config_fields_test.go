/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"reflect"
	"testing"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestParseHTTPSectionFields_SubsetOfFields(t *testing.T) {
	configYAML := `
homeassistant:
  name: Home

http:
  ip_ban_enabled: false
  cors_allowed_origins:
    - https://example.com
  login_attempts_threshold: 5
`
	data, ok := parseHTTPSectionFields(configYAML)
	if !ok {
		t.Fatalf("expected ok=true for a well-formed http: section")
	}
	// ip_ban_enabled is *bool precisely so an explicit "false" in the YAML is
	// distinguishable from the field never being mentioned at all (see
	// haclient.HTTPConfigData's doc comment) — it must come back as a non-nil
	// pointer to false, not nil.
	if data.IPBanEnabled == nil || *data.IPBanEnabled {
		t.Errorf("expected IPBanEnabled=pointer-to-false, got %v", data.IPBanEnabled)
	}
	if data.LoginAttemptsThreshold != 5 {
		t.Errorf("expected LoginAttemptsThreshold=5, got %d", data.LoginAttemptsThreshold)
	}
	if len(data.CORSAllowedOrigins) != 1 || data.CORSAllowedOrigins[0] != "https://example.com" {
		t.Errorf("expected CORSAllowedOrigins=[https://example.com], got %v", data.CORSAllowedOrigins)
	}
	// Fields not present in the YAML must stay zero-valued/nil, never guessed.
	if data.SSLProfile != "" {
		t.Errorf("expected SSLProfile unset, got %q", data.SSLProfile)
	}
	if data.UseXFrameOptions != nil {
		t.Errorf("expected UseXFrameOptions unset (nil), got %v", data.UseXFrameOptions)
	}
	if data.ServerHost != nil {
		t.Errorf("expected ServerHost unset, got %v", data.ServerHost)
	}
}

func TestParseHTTPSectionFields_NoHTTPSection(t *testing.T) {
	configYAML := `
homeassistant:
  name: Home
`
	data, ok := parseHTTPSectionFields(configYAML)
	if !ok {
		t.Fatalf("expected ok=true when http: is absent (empty result, not a parse error)")
	}
	if !reflect.DeepEqual(*data, haclient.HTTPConfigData{}) {
		t.Errorf("expected zero-value HTTPConfigData, got %+v", data)
	}
}

func TestParseHTTPSectionFields_NullHTTPSection(t *testing.T) {
	configYAML := `
homeassistant:
  name: Home
http:
`
	data, ok := parseHTTPSectionFields(configYAML)
	if !ok {
		t.Fatalf("expected ok=true when http: is present but null")
	}
	if !reflect.DeepEqual(*data, haclient.HTTPConfigData{}) {
		t.Errorf("expected zero-value HTTPConfigData, got %+v", data)
	}
}

func TestParseHTTPSectionFields_TaggedScalarUnparseable(t *testing.T) {
	configYAML := `
homeassistant:
  name: Home
http: !include http.yaml
`
	data, ok := parseHTTPSectionFields(configYAML)
	if ok {
		t.Fatalf("expected ok=false for a tagged-scalar http: section, got data=%+v", data)
	}
	if data != nil {
		t.Errorf("expected nil data on parse failure, got %+v", data)
	}
}

func TestParseHTTPSectionFields_InvalidYAML(t *testing.T) {
	data, ok := parseHTTPSectionFields("http: [unterminated")
	if ok {
		t.Fatalf("expected ok=false for unparseable YAML, got data=%+v", data)
	}
	if data != nil {
		t.Errorf("expected nil data on parse failure, got %+v", data)
	}
}

func TestParseHTTPSectionFields_ExplicitFalseBoolsDistinguishedFromUnset(t *testing.T) {
	configYAML := `
http:
  ip_ban_enabled: false
  use_x_frame_options: false
`
	data, ok := parseHTTPSectionFields(configYAML)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if data.IPBanEnabled == nil || *data.IPBanEnabled {
		t.Errorf("expected IPBanEnabled=pointer-to-false (explicitly configured), got %v", data.IPBanEnabled)
	}
	if data.UseXFrameOptions == nil || *data.UseXFrameOptions {
		t.Errorf("expected UseXFrameOptions=pointer-to-false (explicitly configured), got %v", data.UseXFrameOptions)
	}
}

func TestParseHTTPSectionFields_ScalarListFieldsNormalized(t *testing.T) {
	configYAML := `
http:
  server_host: 0.0.0.0
  cors_allowed_origins: https://example.com
`
	data, ok := parseHTTPSectionFields(configYAML)
	if !ok {
		t.Fatalf("expected ok=true for a scalar server_host/cors_allowed_origins, matching HA's own ensure_list schema")
	}
	if len(data.ServerHost) != 1 || data.ServerHost[0] != "0.0.0.0" {
		t.Errorf("expected ServerHost=[0.0.0.0], got %v", data.ServerHost)
	}
	if len(data.CORSAllowedOrigins) != 1 || data.CORSAllowedOrigins[0] != "https://example.com" {
		t.Errorf("expected CORSAllowedOrigins=[https://example.com], got %v", data.CORSAllowedOrigins)
	}
}

func TestParseHTTPSectionFields_InvalidTypedValuePropagatesError(t *testing.T) {
	configYAML := `
http:
  login_attempts_threshold: not-a-number
`
	data, ok := parseHTTPSectionFields(configYAML)
	if ok {
		t.Fatalf("expected ok=false when a typed field cannot be decoded, got data=%+v", data)
	}
	if data != nil {
		t.Errorf("expected nil data on decode failure, got %+v", data)
	}
}

func TestParseHTTPSectionFields_SSLPeerCertificate(t *testing.T) {
	configYAML := `
http:
  ssl_peer_certificate: /config/ssl/client-ca.crt
  use_x_frame_options: true
`
	data, ok := parseHTTPSectionFields(configYAML)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if data.SSLPeerCertificate != "/config/ssl/client-ca.crt" {
		t.Errorf("expected SSLPeerCertificate set, got %q", data.SSLPeerCertificate)
	}
	if data.UseXFrameOptions == nil || !*data.UseXFrameOptions {
		t.Errorf("expected UseXFrameOptions=pointer-to-true, got %v", data.UseXFrameOptions)
	}
}

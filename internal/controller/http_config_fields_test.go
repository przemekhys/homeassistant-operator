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
	if data.IPBanEnabled {
		t.Errorf("expected IPBanEnabled=false, got true")
	}
	if data.LoginAttemptsThreshold != 5 {
		t.Errorf("expected LoginAttemptsThreshold=5, got %d", data.LoginAttemptsThreshold)
	}
	if len(data.CORSAllowedOrigins) != 1 || data.CORSAllowedOrigins[0] != "https://example.com" {
		t.Errorf("expected CORSAllowedOrigins=[https://example.com], got %v", data.CORSAllowedOrigins)
	}
	// Fields not present in the YAML must stay zero-valued, never guessed.
	if data.SSLProfile != "" {
		t.Errorf("expected SSLProfile unset, got %q", data.SSLProfile)
	}
	if data.UseXFrameOptions {
		t.Errorf("expected UseXFrameOptions unset (false), got true")
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
	if !data.UseXFrameOptions {
		t.Errorf("expected UseXFrameOptions=true")
	}
}

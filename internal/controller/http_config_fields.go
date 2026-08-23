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
	"gopkg.in/yaml.v3"

	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// parseHTTPSectionFields parses the http: mapping out of the already-generated
// configuration.yaml content, extracting the fields the operator must forward
// to WS unmodified (server_host, cors_allowed_origins,
// login_attempts_threshold, ip_ban_enabled, ssl_profile, use_x_frame_options,
// ssl_peer_certificate) — everything except ssl_certificate/ssl_key/trusted
// proxies, which desiredHTTPConfigData already manages on its own.
//
// Returns (data, true) when the section was read successfully (data is the
// zero value when http: is absent/null — that's a valid "nothing set" result,
// not a parse failure). Returns (nil, false) when http: is a tagged scalar
// (e.g. "http: !include http.yaml") that cannot be safely introspected — the
// caller MUST treat this the same as WS being unsupported for this reconcile
// rather than send an incomplete guess over WS.
func parseHTTPSectionFields(configYAML string) (*haclient.HTTPConfigData, bool) {
	doc, err := parseConfigYAML(configYAML)
	if err != nil {
		return nil, false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return &haclient.HTTPConfigData{}, true
	}

	httpSection := nodeMappingValue(root, "http")
	switch {
	case httpSection == nil:
		return &haclient.HTTPConfigData{}, true
	case httpSection.Kind == yaml.ScalarNode && httpSection.Value == "" &&
		(httpSection.Tag == "" || httpSection.Tag == "!!null"):
		return &haclient.HTTPConfigData{}, true
	case httpSection.Kind != yaml.MappingNode:
		// Tagged scalar like "http: !include http.yaml" — the user manages
		// this section elsewhere; the operator cannot safely read individual
		// fields out of it.
		return nil, false
	}

	data := &haclient.HTTPConfigData{}
	var decodeErr error
	decode := func(field string, target interface{}) {
		if decodeErr != nil {
			return
		}
		if v := nodeMappingValue(httpSection, field); v != nil {
			decodeErr = v.Decode(target)
		}
	}
	// server_host and cors_allowed_origins accept either a single scalar or a
	// list in HA's own schema (vol.All(cv.ensure_list, [cv.string])) — a bare
	// scalar is normalized to a one-element list, not rejected.
	decodeList := func(field string, target *[]string) {
		if decodeErr != nil {
			return
		}
		v := nodeMappingValue(httpSection, field)
		if v == nil {
			return
		}
		if v.Kind == yaml.ScalarNode {
			var single string
			if decodeErr = v.Decode(&single); decodeErr == nil {
				*target = []string{single}
			}
			return
		}
		decodeErr = v.Decode(target)
	}

	decodeList("server_host", &data.ServerHost)
	decodeList("cors_allowed_origins", &data.CORSAllowedOrigins)
	decode("login_attempts_threshold", &data.LoginAttemptsThreshold)
	decode("ip_ban_enabled", &data.IPBanEnabled)
	decode("ssl_profile", &data.SSLProfile)
	decode("use_x_frame_options", &data.UseXFrameOptions)
	decode("ssl_peer_certificate", &data.SSLPeerCertificate)
	if decodeErr != nil {
		// A field with a type HA itself would reject (e.g. a non-numeric
		// login_attempts_threshold) — never guess or send a partial payload,
		// treat this reconcile the same as WS being unsupported.
		return nil, false
	}
	return data, true
}

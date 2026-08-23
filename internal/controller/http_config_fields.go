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
	if v := nodeMappingValue(httpSection, "server_host"); v != nil {
		_ = v.Decode(&data.ServerHost)
	}
	if v := nodeMappingValue(httpSection, "cors_allowed_origins"); v != nil {
		_ = v.Decode(&data.CORSAllowedOrigins)
	}
	if v := nodeMappingValue(httpSection, "login_attempts_threshold"); v != nil {
		_ = v.Decode(&data.LoginAttemptsThreshold)
	}
	if v := nodeMappingValue(httpSection, "ip_ban_enabled"); v != nil {
		_ = v.Decode(&data.IPBanEnabled)
	}
	if v := nodeMappingValue(httpSection, "ssl_profile"); v != nil {
		_ = v.Decode(&data.SSLProfile)
	}
	if v := nodeMappingValue(httpSection, "use_x_frame_options"); v != nil {
		_ = v.Decode(&data.UseXFrameOptions)
	}
	if v := nodeMappingValue(httpSection, "ssl_peer_certificate"); v != nil {
		_ = v.Decode(&data.SSLPeerCertificate)
	}
	return data, true
}

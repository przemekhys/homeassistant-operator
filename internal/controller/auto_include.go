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
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

// autoIncludeEntries defines the keys and their corresponding !include directives.
// HA expects these files to be explicitly included in configuration.yaml.
var autoIncludeEntries = []struct {
	key      string
	fileName string
}{
	{"automation", "automations.yaml"},
	{"scene", "scenes.yaml"},
	{"script", "scripts.yaml"},
}

// injectLocation injects location fields (latitude, longitude, elevation, time_zone,
// unit_system, name, currency) from spec.bootstrap.location into the homeassistant:
// section of configuration.yaml, but only for fields not already defined by the user.
// Uses yaml.Node to preserve custom YAML tags (e.g. !secret, !include) through
// the unmarshal/marshal round-trip.
// Returns an error if the YAML cannot be parsed or marshalled.
func injectLocation(configYAML string, loc *hav1.LocationConfig) (string, error) {
	if loc == nil {
		return configYAML, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(configYAML), &doc); err != nil {
		return "", fmt.Errorf("failed to parse configuration YAML for location injection: %w", err)
	}

	// Handle empty document
	if doc.Kind == 0 {
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode}},
		}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("expected mapping at root of configuration YAML")
	}

	// Find or create "homeassistant" mapping section
	haSection := nodeMappingValue(root, "homeassistant")
	if haSection == nil {
		haSection = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "homeassistant"},
			haSection,
		)
	} else if haSection.Kind == yaml.ScalarNode && haSection.Value == "" {
		// Empty value (e.g. "homeassistant:\n") — upgrade to mapping in-place
		haSection.Kind = yaml.MappingNode
		haSection.Tag = ""
		haSection.Value = ""
	}
	if haSection.Kind != yaml.MappingNode {
		// homeassistant is not a mapping (e.g. scalar) — cannot inject, return as-is
		out, err := yaml.Marshal(&doc)
		if err != nil {
			return "", fmt.Errorf("failed to marshal configuration YAML: %w", err)
		}
		return string(out), nil
	}

	// Inject location fields only if not already defined by user
	log := logf.Log.WithName("injectLocation")
	if loc.Latitude != "" {
		if _, err := strconv.ParseFloat(loc.Latitude, 64); err != nil {
			log.Error(err, "Invalid latitude value, skipping", "latitude", loc.Latitude)
		} else {
			setNodeField(haSection, "latitude", loc.Latitude, "!!float")
		}
	}
	if loc.Longitude != "" {
		if _, err := strconv.ParseFloat(loc.Longitude, 64); err != nil {
			log.Error(err, "Invalid longitude value, skipping", "longitude", loc.Longitude)
		} else {
			setNodeField(haSection, "longitude", loc.Longitude, "!!float")
		}
	}
	if loc.Elevation != nil {
		setNodeField(haSection, "elevation", strconv.Itoa(*loc.Elevation), "!!int")
	}
	if loc.UnitSystem != "" {
		setNodeField(haSection, "unit_system", loc.UnitSystem, "!!str")
	}
	if loc.TimeZone != "" {
		setNodeField(haSection, "time_zone", loc.TimeZone, "!!str")
	}
	if loc.Name != "" {
		setNodeField(haSection, "name", loc.Name, "!!str")
	}
	if loc.Currency != "" {
		setNodeField(haSection, "currency", loc.Currency, "!!str")
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal configuration YAML after location injection: %w", err)
	}
	return string(out), nil
}

// nodeMappingValue returns the value node for a given key in a mapping node, or nil.
func nodeMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// setNodeField adds a key-value pair to a mapping node if the key doesn't already exist.
func setNodeField(mapping *yaml.Node, key, value, tag string) {
	if nodeMappingValue(mapping, key) != nil {
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: tag},
	)
}

// overrideNodeField sets a key-value pair in a mapping node, replacing any existing value.
func overrideNodeField(mapping *yaml.Node, key, value, tag string) {
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Kind = yaml.ScalarNode
			mapping.Content[i+1].Tag = tag
			mapping.Content[i+1].Value = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: tag},
	)
}

// setSequenceFieldIfAbsent adds a key with a list-of-strings value to a mapping
// node if the key doesn't already exist. Like setNodeField (its scalar
// counterpart), it never touches an existing key — used for trusted_proxies,
// which is a YAML sequence rather than a scalar.
func setSequenceFieldIfAbsent(mapping *yaml.Node, key string, values []string) {
	if nodeMappingValue(mapping, key) != nil {
		return
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v, Tag: "!!str"})
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		seq,
	)
}

// shouldInjectRecorder reports whether injectRecorder has any fields to write.
// Returns false when recorder is nil, explicitly disabled, or there is nothing to inject.
func shouldInjectRecorder(recorder *hav1.RecorderConfig, dbURL string) bool {
	if recorder == nil {
		return false
	}
	if recorder.Enabled != nil && !*recorder.Enabled {
		return false
	}
	return dbURL != "" || recorder.PurgeKeepDays != nil
}

// parseConfigYAML parses configYAML into a yaml.DocumentNode.
// Returns an empty document node when the input is empty or null.
func parseConfigYAML(configYAML string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(configYAML), &doc); err != nil {
		return nil, fmt.Errorf("failed to parse configuration YAML for recorder injection: %w", err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode}},
		}
	}
	return &doc, nil
}

// getOrCreateRecorderSection finds or creates the "recorder" mapping under root.
// Returns (section, true) when the section is a mapping node that can be mutated.
// Returns (nil, false) when the section is a tagged scalar (e.g. "!include recorder.yaml")
// that must be preserved unchanged — the caller should return configYAML as-is.
func getOrCreateRecorderSection(root *yaml.Node) (*yaml.Node, bool) {
	recSection := nodeMappingValue(root, "recorder")
	if recSection == nil {
		recSection = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "recorder"},
			recSection,
		)
		return recSection, true
	}
	// Bare "recorder:" with truly empty/null value — upgrade to mapping in-place.
	if recSection.Kind == yaml.ScalarNode &&
		recSection.Value == "" &&
		(recSection.Tag == "" || recSection.Tag == "!!null") {
		recSection.Kind = yaml.MappingNode
		recSection.Tag = ""
		recSection.Value = ""
		return recSection, true
	}
	if recSection.Kind != yaml.MappingNode {
		// Tagged scalar like "recorder: !include recorder.yaml" — preserve unchanged.
		return nil, false
	}
	return recSection, true
}

// applyRecorderFields writes db_url and purge_keep_days into the recorder mapping node.
// When useInclude is true, db_url is written as "!include recorder_db_url.yaml" so that
// credentials are not materialised into the ConfigMap; the actual URL must be stored in a
// K8s Secret mounted at /config/recorder_db_url.yaml (see reconcileRecorderDBSecret).
// When useInclude is false, dbURL is written verbatim as a plain string.
func applyRecorderFields(recSection *yaml.Node, dbURL string, useInclude bool, recorder *hav1.RecorderConfig) {
	if dbURL != "" {
		if useInclude {
			overrideNodeField(recSection, "db_url", "recorder_db_url.yaml", "!include")
		} else {
			overrideNodeField(recSection, "db_url", dbURL, "!!str")
		}
	}
	if recorder.PurgeKeepDays != nil {
		overrideNodeField(recSection, "purge_keep_days", strconv.Itoa(int(*recorder.PurgeKeepDays)), "!!int")
	}
}

// injectRecorder merges spec.recorder fields into the recorder: section of
// configuration.yaml. When useInclude is true, db_url is written as
// "!include recorder_db_url.yaml" instead of the literal URL; the actual URL
// must be stored in a K8s Secret mounted at /config/recorder_db_url.yaml so
// that credentials are never placed in a ConfigMap. Uses yaml.Node to preserve
// !include / !secret tags in other sections through the round-trip.
// Returns configYAML unchanged when recorder is nil or disabled.
func injectRecorder(
	configYAML string, recorder *hav1.RecorderConfig, dbURL string, useInclude bool,
) (string, error) {
	if !shouldInjectRecorder(recorder, dbURL) {
		return configYAML, nil
	}

	doc, err := parseConfigYAML(configYAML)
	if err != nil {
		return "", err
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("expected mapping at root of configuration YAML")
	}

	recSection, ok := getOrCreateRecorderSection(root)
	if !ok {
		// Tagged scalar preserved — return input unchanged.
		return configYAML, nil
	}

	applyRecorderFields(recSection, dbURL, useInclude, recorder)

	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal configuration YAML after recorder injection: %w", err)
	}
	return string(out), nil
}

// injectNativeTLS sets http.ssl_certificate/ssl_key so Home Assistant serves HTTPS
// natively from the certificate mounted at /config/ssl (native TLS mode). Uses
// yaml.Node to preserve !include / !secret tags elsewhere. A tagged-scalar http:
// section (e.g. "http: !include http.yaml") is preserved unchanged.
func injectNativeTLS(configYAML string) (string, error) {
	doc, err := parseConfigYAML(configYAML)
	if err != nil {
		return "", err
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return configYAML, nil
	}

	httpSection := nodeMappingValue(root, "http")
	switch {
	case httpSection == nil:
		httpSection = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "http"},
			httpSection,
		)
	case httpSection.Kind == yaml.ScalarNode && httpSection.Value == "" &&
		(httpSection.Tag == "" || httpSection.Tag == "!!null"):
		httpSection.Kind = yaml.MappingNode
		httpSection.Tag = ""
	case httpSection.Kind != yaml.MappingNode:
		// Tagged scalar like "http: !include http.yaml" — preserve unchanged.
		return configYAML, nil
	}

	overrideNodeField(httpSection, "ssl_certificate", nativeTLSCertPath, "!!str")
	overrideNodeField(httpSection, "ssl_key", nativeTLSKeyPath, "!!str")

	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal configuration YAML after native TLS injection: %w", err)
	}
	return string(out), nil
}

// buildEffectiveConfig applies injectLocation and ensureAutoIncludes transformations
// to produce the final configuration.yaml content written to the ConfigMap.
// ha may be nil (no location injection) if the HomeAssistant CR is unavailable.
// If location injection fails (unexpected YAML parse error), the error is logged and
// the function falls back to rawConfig before appending auto-include directives.
func buildEffectiveConfig(rawConfig string, ha *hav1.HomeAssistant) string {
	var loc *hav1.LocationConfig
	if ha != nil && ha.Spec.Bootstrap != nil {
		loc = ha.Spec.Bootstrap.Location
	}
	injected, err := injectLocation(rawConfig, loc)
	if err != nil {
		logf.Log.WithName("buildEffectiveConfig").Error(err, "Location injection failed, proceeding without location")
		injected = rawConfig
	}
	return ensureAutoIncludes(injected)
}

// ensureAutoIncludes adds `!include` directives for automation, scene, and script
// if they are not already present in the configuration YAML.
// Uses YAML parsing only for reading top-level keys; appends raw text to preserve
// HA's custom `!include` tag (which is not standard YAML).
func ensureAutoIncludes(configYAML string) string {
	// Parse YAML to get top-level keys
	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &parsed); err != nil {
		// Safe fallback: return input unchanged on parse error
		return configYAML
	}

	if parsed == nil {
		parsed = make(map[string]interface{})
	}

	result := configYAML
	var additions []string
	for _, entry := range autoIncludeEntries {
		val, exists := parsed[entry.key]
		if !exists {
			additions = append(additions, entry.key+": !include "+entry.fileName)
		} else if strVal, ok := val.(string); ok && strVal == entry.fileName {
			// Bare filename without !include tag — lost during YAML round-trip
			// (e.g. injectLocation's yaml.Marshal strips !include tags).
			// Fix in-place by restoring the !include directive.
			result = strings.Replace(
				result,
				entry.key+": "+entry.fileName,
				entry.key+": !include "+entry.fileName,
				1,
			)
		}
	}

	if len(additions) == 0 && result == configYAML {
		return configYAML
	}

	if len(additions) > 0 {
		// Ensure trailing newline before appending
		if result != "" && !strings.HasSuffix(result, "\n") {
			result += "\n"
		}
		result += strings.Join(additions, "\n") + "\n"
	}
	return result
}

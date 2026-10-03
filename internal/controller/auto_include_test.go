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
	"strings"
	"testing"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

func TestEnsureAutoIncludes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:  "empty config adds all three includes",
			input: "",
			expected: "automation: !include automations.yaml\n" +
				"scene: !include scenes.yaml\n" +
				"script: !include scripts.yaml\n",
		},
		{
			name:  "config with only homeassistant section adds all three",
			input: "homeassistant:\n  name: My Home\n",
			expected: "homeassistant:\n  name: My Home\n" +
				"automation: !include automations.yaml\n" +
				"scene: !include scenes.yaml\n" +
				"script: !include scripts.yaml\n",
		},
		{
			name:     "config with automation key does not duplicate",
			input:    "automation: !include automations.yaml\n",
			expected: "automation: !include automations.yaml\nscene: !include scenes.yaml\nscript: !include scripts.yaml\n",
		},
		{
			name:     "config with automation as empty list does not override",
			input:    "automation: []\n",
			expected: "automation: []\nscene: !include scenes.yaml\nscript: !include scripts.yaml\n",
		},
		{
			name:     "config with all three keys present returns unchanged",
			input:    "automation: !include automations.yaml\nscene: !include scenes.yaml\nscript: !include scripts.yaml\n",
			expected: "automation: !include automations.yaml\nscene: !include scenes.yaml\nscript: !include scripts.yaml\n",
		},
		{
			name:     "invalid YAML returns input unchanged",
			input:    "invalid: yaml: :\n  - [broken",
			expected: "invalid: yaml: :\n  - [broken",
		},
		{
			name: "idempotent - running twice gives same result",
			input: "homeassistant:\n  name: Test\n" +
				"automation: !include automations.yaml\n" +
				"scene: !include scenes.yaml\n" +
				"script: !include scripts.yaml\n",
			expected: "homeassistant:\n  name: Test\n" +
				"automation: !include automations.yaml\n" +
				"scene: !include scenes.yaml\n" +
				"script: !include scripts.yaml\n",
		},
		{
			name:  "config with inline automation definition does not add include",
			input: "automation:\n  - alias: Test\n    trigger: []\n",
			expected: "automation:\n  - alias: Test\n    trigger: []\n" +
				"scene: !include scenes.yaml\n" +
				"script: !include scripts.yaml\n",
		},
		{
			name:  "config without trailing newline gets newline added",
			input: "homeassistant:\n  name: Test",
			expected: "homeassistant:\n  name: Test\n" +
				"automation: !include automations.yaml\n" +
				"scene: !include scenes.yaml\n" +
				"script: !include scripts.yaml\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureAutoIncludes(tt.input)
			if result != tt.expected {
				t.Errorf("ensureAutoIncludes() =\n%q\nwant:\n%q", result, tt.expected)
			}
		})
	}
}

func TestInjectLocation(t *testing.T) {
	elevation := 100
	loc := &hav1.LocationConfig{
		Latitude:   "52.2297",
		Longitude:  "21.0122",
		Elevation:  &elevation,
		UnitSystem: "metric",
		TimeZone:   "Europe/Warsaw",
		Name:       "Home",
		Currency:   "PLN",
	}

	t.Run("injects country", func(t *testing.T) {
		withCountry := *loc
		withCountry.Country = "PL"
		result, err := injectLocation("homeassistant:\n  name: My Home\n", &withCountry)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "country: PL") {
			t.Errorf("country not injected, got:\n%s", result)
		}
	})

	t.Run("preserves !secret tags", func(t *testing.T) {
		input := "homeassistant:\n  name: My Home\nrecorder:\n  db_url: !secret recorder_db_url\n"
		result, err := injectLocation(input, loc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "!secret recorder_db_url") {
			t.Errorf("!secret tag was stripped, got:\n%s", result)
		}
		// name already defined by user — should not be overwritten
		if !strings.Contains(result, "name: My Home") {
			t.Errorf("user-defined name was overwritten, got:\n%s", result)
		}
	})

	t.Run("preserves !include tags", func(t *testing.T) {
		input := "automation: !include automations.yaml\n"
		result, err := injectLocation(input, loc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "!include automations.yaml") {
			t.Errorf("!include tag was stripped, got:\n%s", result)
		}
	})

	t.Run("nil location returns input unchanged", func(t *testing.T) {
		input := "recorder:\n  db_url: !secret recorder_db_url\n"
		result, err := injectLocation(input, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != input {
			t.Errorf("expected unchanged input, got:\n%s", result)
		}
	})

	t.Run("injects location fields into empty homeassistant section", func(t *testing.T) {
		input := "homeassistant:\n"
		result, err := injectLocation(input, loc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "latitude: 52.2297") {
			t.Errorf("latitude not injected, got:\n%s", result)
		}
		if !strings.Contains(result, "longitude: 21.0122") {
			t.Errorf("longitude not injected, got:\n%s", result)
		}
		if !strings.Contains(result, "elevation: 100") {
			t.Errorf("elevation not injected, got:\n%s", result)
		}
	})

	t.Run("does not overwrite existing fields", func(t *testing.T) {
		input := "homeassistant:\n  latitude: 50.0\n  name: Custom\n"
		result, err := injectLocation(input, loc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "latitude: 50") {
			t.Errorf("existing latitude was overwritten, got:\n%s", result)
		}
		if !strings.Contains(result, "name: Custom") {
			t.Errorf("existing name was overwritten, got:\n%s", result)
		}
	})

	t.Run("creates homeassistant section if missing", func(t *testing.T) {
		input := "recorder:\n  purge_keep_days: 5\n"
		result, err := injectLocation(input, loc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "homeassistant:") {
			t.Errorf("homeassistant section not created, got:\n%s", result)
		}
		if !strings.Contains(result, "latitude: 52.2297") {
			t.Errorf("latitude not injected, got:\n%s", result)
		}
	})
}

func TestEnsureAutoIncludes_Idempotent(t *testing.T) {
	input := "homeassistant:\n  name: Test\n"
	first := ensureAutoIncludes(input)
	second := ensureAutoIncludes(first)
	if first != second {
		t.Errorf("ensureAutoIncludes is not idempotent:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestInjectRecorder(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		recorder   *hav1.RecorderConfig
		dbURL      string
		useInclude bool
		wantIn     []string
		wantNotIn  []string
		wantErr    bool
	}{
		{
			name:     "nil recorder — input unchanged",
			input:    "homeassistant:\n  name: Test\n",
			recorder: nil,
			wantIn:   []string{"homeassistant:"},
		},
		{
			name:      "disabled recorder — input unchanged",
			input:     "homeassistant:\n  name: Test\n",
			recorder:  &hav1.RecorderConfig{Enabled: boolPtr(false)},
			dbURL:     "postgresql://user:pass@host/db",
			wantNotIn: []string{"recorder:"},
		},
		{
			name:     "dbURL injected into new recorder section",
			input:    "homeassistant:\n  name: Test\n",
			recorder: &hav1.RecorderConfig{},
			dbURL:    "postgresql://user:pass@host/db",
			wantIn:   []string{"recorder:", "db_url: postgresql://user:pass@host/db"},
		},
		{
			name:     "purge_keep_days injected",
			input:    "homeassistant:\n  name: Test\n",
			recorder: &hav1.RecorderConfig{PurgeKeepDays: int32Ptr(7)},
			dbURL:    "postgresql://user:pass@host/db",
			wantIn:   []string{"purge_keep_days: 7"},
		},
		{
			name:      "existing recorder db_url overridden",
			input:     "recorder:\n  db_url: sqlite://old\n",
			recorder:  &hav1.RecorderConfig{},
			dbURL:     "postgresql://user:pass@host/db",
			wantIn:    []string{"db_url: postgresql://user:pass@host/db"},
			wantNotIn: []string{"sqlite://old"},
		},
		{
			name:     "!include tags in other sections preserved",
			input:    "automation: !include automations.yaml\nscript: !include scripts.yaml\n",
			recorder: &hav1.RecorderConfig{},
			dbURL:    "postgresql://user:pass@host/db",
			wantIn:   []string{"!include automations.yaml", "!include scripts.yaml", "db_url:"},
		},
		{
			name:      "empty dbURL and nil PurgeKeepDays — input unchanged",
			input:     "homeassistant:\n  name: Test\n",
			recorder:  &hav1.RecorderConfig{},
			dbURL:     "",
			wantNotIn: []string{"recorder:"},
		},
		{
			// Regression: tagged scalar like "recorder: !include recorder.yaml" must not be
			// overwritten. injectRecorder should return configYAML unchanged.
			name:     "tagged recorder scalar preserved (!include recorder.yaml)",
			input:    "recorder: !include recorder.yaml\n",
			recorder: &hav1.RecorderConfig{},
			dbURL:    "postgresql://user:pass@host/db",
			wantIn:   []string{"recorder: !include recorder.yaml"},
		},
		{
			// Security: when useInclude=true the URL must NOT appear in the output; instead
			// db_url should reference the mounted file via HA's !include tag.
			name:       "useInclude writes !include reference instead of URL",
			input:      "homeassistant:\n  name: Test\n",
			recorder:   &hav1.RecorderConfig{},
			dbURL:      "postgresql://user:pass@host/db",
			useInclude: true,
			wantIn:     []string{"db_url: !include recorder_db_url.yaml"},
			wantNotIn:  []string{"postgresql://"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := injectRecorder(tt.input, tt.recorder, tt.dbURL, tt.useInclude)
			if (err != nil) != tt.wantErr {
				t.Fatalf("injectRecorder() error = %v, wantErr %v", err, tt.wantErr)
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in output:\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNotIn {
				if strings.Contains(got, notWant) {
					t.Errorf("did not expect %q in output:\n%s", notWant, got)
				}
			}
		})
	}
}

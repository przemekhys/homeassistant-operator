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

package communityrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Category is a HACS repository category. Values match HACS's own hacs.json
// "category" field exactly. Declared locally (not imported from api/v1alpha1) to
// keep this package free of any dependency beyond the Go standard library.
type Category string

const (
	CategoryIntegration  Category = "integration"
	CategoryPlugin       Category = "plugin"
	CategoryTheme        Category = "theme"
	CategoryPythonScript Category = "python_script"
	CategoryTemplate     Category = "template"
)

// Sentinel errors — wrapped with context by callers, matched with errors.Is.
var (
	ErrRepositoryUnreachable = errors.New("repository unreachable")
	ErrCategoryMismatch      = errors.New("repository category does not match requested category")
	ErrStructureInvalid      = errors.New("repository does not follow the expected HACS structure for this category")
)

// hacsManifest is the subset of hacs.json this package reads.
type hacsManifest struct {
	Name     string `json:"name,omitempty"`
	Category string `json:"category,omitempty"`
	Filename string `json:"filename,omitempty"` // optional, used by the plugin category when multiple .js files exist
}

// componentManifest is the subset of a Home Assistant custom_components/*/manifest.json this package reads.
type componentManifest struct {
	Domain string `json:"domain"`
}

// Resolved holds the outcome of validating a fetched repository against a requested
// category: where its content lives inside the fetched tree (SourcePath) and what
// it should be installed as (ResolvedTarget).
type Resolved struct {
	// SourcePath is the path (relative to the fetched repo root) to copy into place:
	// a directory for "integration", a single file for the other four categories.
	SourcePath string
	// ResolvedTarget is the install target name: the integration's domain, or the
	// theme/plugin/python_script/template's file name (without extension).
	ResolvedTarget string
}

// ValidateAndResolve reads the fetched repo's hacs.json (if present) and the
// category-specific structure, and returns the resolved install target. It never
// installs anything itself — this is read-only inspection of an already-fetched,
// in-memory tree (see FetchTarball).
func ValidateAndResolve(repo *ExtractedRepo, category Category) (Resolved, error) {
	manifest, err := readHACSManifest(repo)
	if err != nil {
		return Resolved{}, err
	}
	if manifest != nil && manifest.Category != "" && Category(manifest.Category) != category {
		return Resolved{}, fmt.Errorf("%w: repository declares %q, requested %q",
			ErrCategoryMismatch, manifest.Category, category)
	}

	switch category {
	case CategoryIntegration:
		return resolveIntegration(repo)
	case CategoryPlugin:
		return resolvePlugin(repo, manifest)
	case CategoryTheme:
		return resolveSingleFile(repo, "themes", []string{".yaml", ".yml"})
	case CategoryPythonScript:
		return resolveSingleFile(repo, "python_scripts", []string{".py"})
	case CategoryTemplate:
		return resolveSingleFile(repo, "custom_templates", nil)
	default:
		return Resolved{}, fmt.Errorf("%w: unsupported category %q", ErrStructureInvalid, category)
	}
}

// readHACSManifest reads hacs.json from the repository root. Its absence is not an
// error — hacs.json's "category" field is optional/advisory (HACS itself infers
// category from directory structure alone when the field is missing), so callers
// treat a nil manifest as "no advisory category to cross-check".
func readHACSManifest(repo *ExtractedRepo) (*hacsManifest, error) {
	data, err := repo.ReadFile("hacs.json")
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read hacs.json: %w", err)
	}
	var m hacsManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: hacs.json is not valid JSON: %v", ErrStructureInvalid, err)
	}
	return &m, nil
}

// resolveIntegration finds the single custom_components/*/manifest.json and returns
// its "domain" as the resolved target.
func resolveIntegration(repo *ExtractedRepo) (Resolved, error) {
	entries, err := repo.ReadDir("custom_components")
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: no custom_components/ directory: %v", ErrStructureInvalid, err)
	}

	var componentDirs []string
	for _, e := range entries {
		if e.IsDir {
			componentDirs = append(componentDirs, e.Name)
		}
	}
	if len(componentDirs) != 1 {
		return Resolved{}, fmt.Errorf("%w: expected exactly one directory under custom_components/, found %d",
			ErrStructureInvalid, len(componentDirs))
	}

	manifestPath := path.Join("custom_components", componentDirs[0], "manifest.json")
	data, err := repo.ReadFile(manifestPath)
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: missing manifest.json under custom_components/%s/: %v",
			ErrStructureInvalid, componentDirs[0], err)
	}
	var cm componentManifest
	if err := json.Unmarshal(data, &cm); err != nil {
		return Resolved{}, fmt.Errorf("%w: manifest.json is not valid JSON: %v", ErrStructureInvalid, err)
	}
	if cm.Domain == "" {
		return Resolved{}, fmt.Errorf("%w: manifest.json is missing the required domain field", ErrStructureInvalid)
	}

	return Resolved{
		SourcePath:     path.Join("custom_components", componentDirs[0]),
		ResolvedTarget: cm.Domain,
	}, nil
}

// resolvePlugin finds the plugin's main JavaScript file: hacs.json's "filename" field
// when set (preferring dist/ for a basename), otherwise the single ".js" file at
// the repository root.
func resolvePlugin(repo *ExtractedRepo, manifest *hacsManifest) (Resolved, error) {
	if manifest != nil && manifest.Filename != "" {
		sourcePath := manifest.Filename
		if path.Base(sourcePath) == sourcePath {
			distPath := path.Join("dist", sourcePath)
			// Only regular files qualify; large bundles may have no retained content.
			if _, ok := repo.files[distPath]; ok {
				sourcePath = distPath
			}
		}
		if !repo.Exists(sourcePath) {
			return Resolved{}, fmt.Errorf("%w: hacs.json filename %q not found",
				ErrStructureInvalid, manifest.Filename)
		}
		return Resolved{
			SourcePath:     sourcePath,
			ResolvedTarget: strings.TrimSuffix(path.Base(manifest.Filename), path.Ext(manifest.Filename)),
		}, nil
	}
	return resolveSingleFile(repo, "", []string{".js"})
}

// resolveSingleFile finds exactly one file with one of the given extensions under
// subdir (or the repository root when subdir is ""). When extensions is nil,
// any regular file is accepted (used by the "template" category, which HACS does
// not restrict to a single extension).
func resolveSingleFile(repo *ExtractedRepo, subdir string, extensions []string) (Resolved, error) {
	dir := subdir
	if dir == "" {
		dir = "."
	}
	entries, err := repo.ReadDir(dir)
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: no %s/ directory: %v", ErrStructureInvalid, subdir, err)
	}

	var matches []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if len(extensions) == 0 {
			matches = append(matches, e.Name)
			continue
		}
		for _, ext := range extensions {
			if strings.EqualFold(path.Ext(e.Name), ext) {
				matches = append(matches, e.Name)
				break
			}
		}
	}

	if len(matches) != 1 {
		return Resolved{}, fmt.Errorf("%w: expected exactly one matching file in %s/, found %d",
			ErrStructureInvalid, subdir, len(matches))
	}

	sourcePath := matches[0]
	if subdir != "" {
		sourcePath = path.Join(subdir, matches[0])
	}
	return Resolved{
		SourcePath:     sourcePath,
		ResolvedTarget: strings.TrimSuffix(matches[0], path.Ext(matches[0])),
	}, nil
}

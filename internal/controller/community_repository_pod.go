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
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

const (
	communityRepositoriesVolumeName  = "community-repositories"
	communityRepositoriesMountPath   = "/etc/community-repositories"
	communityRepositoriesFileEnv     = "COMMUNITY_REPOSITORIES_FILE"
	communityRepositoriesFilePath    = communityRepositoriesMountPath + "/repositories.json"
	communityRepositoryPollIntervalS = "30"
)

// hasCommunityRepositories reports whether at least one HomeAssistantCommunityRepository
// targets ha, which gates the sidecar/init-container/ConfigMap-volume injection —
// the stable HomeAssistant CRD never gets this alpha-feature footprint unless the
// alpha CRD is actually in use. A List error is propagated rather than treated as
// "no": silently degrading to "no repositories" on a transient API error would let
// callers build a StatefulSet spec that strips an already-installed sidecar, doing
// an unwanted rolling restart the moment the API server hiccups.
func hasCommunityRepositories(ctx context.Context, c client.Client, ha *hav1.HomeAssistant) (bool, error) {
	list := &hav1alpha1.HomeAssistantCommunityRepositoryList{}
	if err := c.List(ctx, list, client.InNamespace(ha.Namespace)); err != nil {
		return false, err
	}
	for _, item := range list.Items {
		if item.Spec.HomeAssistantRef.Name == ha.Name {
			return true, nil
		}
	}
	return false, nil
}

// buildCommunityRepositoryConfigMapVolume returns the read-only volume exposing the
// aggregate ConfigMap to the init-container and sidecar. Optional=true so pod
// creation never blocks on a race with HomeAssistantCommunityRepositoryReconciler
// creating the ConfigMap (same pattern as operatorIPConfigMapKey).
func buildCommunityRepositoryConfigMapVolume(ha *hav1.HomeAssistant) corev1.Volume {
	optional := true
	return corev1.Volume{
		Name: communityRepositoriesVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: communityRepositoriesConfigMapName(ha.Name),
				},
				Optional: &optional,
			},
		},
	}
}

func communityRepositoryVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      communityRepositoriesVolumeName,
		MountPath: communityRepositoriesMountPath,
		ReadOnly:  true,
	}
}

// buildCommunityRepositoryInitContainer materializes "integration" category
// repositories into /config/custom_components/ before Home Assistant starts —
// the one category that requires a restart to load, so it must be in place before
// the main container's Python process starts importing components. Reuses the HA
// image (already cached on the node, already has Python) rather than pulling a new
// image, exactly like buildUnbanInitContainer.
func (r *HomeAssistantReconciler) buildCommunityRepositoryInitContainer(ha *hav1.HomeAssistant) corev1.Container {
	haImage := homeAssistantImageRef(ha)

	return corev1.Container{
		Name:            "community-repository-init",
		Image:           haImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"python3", "-u", "-c", communityRepositoryInitScript},
		Env:             append(communityRepositoryBaseEnv(), corev1.EnvVar{Name: "HA_CONFIG_DIR", Value: "/config"}),
		VolumeMounts: []corev1.VolumeMount{
			{Name: "config", MountPath: "/config"},
			communityRepositoryVolumeMount(),
		},
	}
}

// communityRepositoryBaseEnv returns the env vars shared by the init-container and
// sidecar. CODELOAD_BASE_URL is only set (overriding the Python scripts' own
// "https://codeload.github.com" default) when the operator itself was started with
// COMMUNITY_REPOSITORY_CODELOAD_BASE_URL set — used exclusively by E2E tests to
// redirect fetches to an in-cluster fixture server; production deployments never
// set this and get the real GitHub host.
func communityRepositoryBaseEnv() []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: communityRepositoriesFileEnv, Value: communityRepositoriesFilePath},
	}
	if base := os.Getenv("COMMUNITY_REPOSITORY_CODELOAD_BASE_URL"); base != "" {
		env = append(env, corev1.EnvVar{Name: "CODELOAD_BASE_URL", Value: base})
	}
	return env
}

// buildCommunityRepositorySidecar runs alongside the main HA container for the
// pod's whole lifetime, polling the aggregate ConfigMap every ~30s and
// materializing/removing files for the four categories that don't require a
// restart (theme, python_script, template, plugin). It never calls the Kubernetes
// API and never calls the Home Assistant API — activation (reload/Lovelace
// registration) is exclusively the operator's job, once it confirms the file
// landed. Reuses the HA image, same rationale as the init container above.
func (r *HomeAssistantReconciler) buildCommunityRepositorySidecar(ha *hav1.HomeAssistant) corev1.Container {
	haImage := homeAssistantImageRef(ha)

	return corev1.Container{
		Name:            "community-repository-sidecar",
		Image:           haImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"python3", "-u", "-c", communityRepositorySidecarScript},
		Env: append(communityRepositoryBaseEnv(),
			corev1.EnvVar{Name: "HA_CONFIG_DIR", Value: "/config"},
			corev1.EnvVar{Name: "COMMUNITY_REPOSITORIES_POLL_INTERVAL", Value: communityRepositoryPollIntervalS},
		),
		VolumeMounts: []corev1.VolumeMount{
			{Name: "config", MountPath: "/config"},
			communityRepositoryVolumeMount(),
		},
		// Lightweight by design: the target platform is k3s on ARM64 single-board
		// machines, where every reserved megabyte competes with Home Assistant
		// itself. This is a small polling file-sync loop, not a service.
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
	}
}

// homeAssistantImageRef returns the same image:version HA itself runs, so the
// init-container/sidecar are always guaranteed to have Python and are already
// cached on the node (no extra image pull).
func homeAssistantImageRef(ha *hav1.HomeAssistant) string {
	image := defaultImage
	if ha.Spec.Image != "" {
		image = ha.Spec.Image
	}
	version := defaultVersion
	if ha.Spec.Version != "" {
		version = ha.Spec.Version
	}
	return fmt.Sprintf("%s:%s", image, version)
}

// communityRepositoryInitScript materializes "integration"-category entries only
// (see buildCommunityRepositoryInitContainer doc comment). Stdlib-only (no pip
// deps): json, os, shutil, sys, tarfile, tempfile, urllib.request.
const communityRepositoryInitScript = `
import json, os, shutil, sys, tarfile, tempfile, urllib.request

REPO_FILE = os.environ.get('COMMUNITY_REPOSITORIES_FILE', '/etc/community-repositories/repositories.json')
CONFIG_DIR = os.environ.get('HA_CONFIG_DIR', '/config')
CODELOAD_BASE = os.environ.get('CODELOAD_BASE_URL', 'https://codeload.github.com')

# HACS repos are tiny plain-text source trees; these bound worst-case disk use
# against a malicious or misbehaving upstream repository/host.
MAX_DOWNLOAD_BYTES = 100 * 1024 * 1024
MAX_EXTRACTED_BYTES = 100 * 1024 * 1024


def load_entries():
    if not os.path.exists(REPO_FILE):
        return []
    try:
        with open(REPO_FILE) as f:
            data = json.load(f)
    except (OSError, ValueError):
        return []
    return [e for e in data.get('repositories', []) if e.get('category') == 'integration']


def copy_limited(src, dst, limit):
    copied = 0
    while True:
        chunk = src.read(65536)
        if not chunk:
            return
        copied += len(chunk)
        if copied > limit:
            raise RuntimeError('download exceeds {} byte limit'.format(limit))
        dst.write(chunk)


def safe_extractall(tar, path):
    abs_path = os.path.abspath(path)
    total = 0
    for member in tar.getmembers():
        member_path = os.path.abspath(os.path.join(path, member.name))
        if not (member_path == abs_path or member_path.startswith(abs_path + os.sep)):
            raise RuntimeError('unsafe path in tarball: ' + member.name)
        if member.isfile():
            total += member.size
            if total > MAX_EXTRACTED_BYTES:
                raise RuntimeError('extracted content exceeds {} byte limit'.format(MAX_EXTRACTED_BYTES))
    try:
        tar.extractall(path, filter='data')
    except TypeError:
        tar.extractall(path)


def fetch_and_place(entry):
    url = "{}/{}/tar.gz/{}".format(CODELOAD_BASE, entry['repository'], entry['ref'])
    with urllib.request.urlopen(url, timeout=30) as resp:
        with tempfile.TemporaryDirectory() as tmp:
            tar_path = os.path.join(tmp, 'repo.tar.gz')
            with open(tar_path, 'wb') as f:
                copy_limited(resp, f, MAX_DOWNLOAD_BYTES)
            extract_dir = os.path.join(tmp, 'extracted')
            os.makedirs(extract_dir, exist_ok=True)
            with tarfile.open(tar_path, 'r:gz') as tar:
                safe_extractall(tar, extract_dir)
            top_level = os.listdir(extract_dir)
            if len(top_level) != 1:
                raise RuntimeError('expected a single top-level directory, found {}'.format(len(top_level)))
            root = os.path.join(extract_dir, top_level[0])
            src = os.path.join(root, entry['sourcePath'])
            dest = os.path.join(CONFIG_DIR, 'custom_components', entry['resolvedTarget'])
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            if os.path.isdir(dest):
                shutil.rmtree(dest)
            elif os.path.exists(dest):
                os.remove(dest)
            shutil.copytree(src, dest)


def main():
    for entry in load_entries():
        try:
            fetch_and_place(entry)
            print('materialized integration {} from {}@{}'.format(
                entry['resolvedTarget'], entry['repository'], entry['ref']))
        except Exception as e:
            print('WARNING: failed to materialize integration {}: {}'.format(
                entry.get('resolvedTarget'), e), file=sys.stderr)


if __name__ == '__main__':
    main()
`

// communityRepositorySidecarScript polls the aggregate ConfigMap every
// COMMUNITY_REPOSITORIES_POLL_INTERVAL seconds and materializes/removes files for
// the four non-restart categories. State (which ref is currently installed at
// which path) is tracked in a small JSON file on the shared /config PVC so it
// survives sidecar container restarts without needing any Kubernetes API access.
const communityRepositorySidecarScript = `
import json, os, shutil, sys, tarfile, tempfile, time, urllib.request

REPO_FILE = os.environ.get('COMMUNITY_REPOSITORIES_FILE', '/etc/community-repositories/repositories.json')
CONFIG_DIR = os.environ.get('HA_CONFIG_DIR', '/config')
CODELOAD_BASE = os.environ.get('CODELOAD_BASE_URL', 'https://codeload.github.com')
STATE_FILE = os.path.join(CONFIG_DIR, '.community_repositories_sidecar_state.json')
POLL_INTERVAL_SECONDS = int(os.environ.get('COMMUNITY_REPOSITORIES_POLL_INTERVAL', '30'))

CATEGORY_DIRS = {
    'theme': 'themes',
    'python_script': 'python_scripts',
    'template': 'custom_templates',
    'plugin': 'www/community',
}

# HACS repos are tiny plain-text source trees; these bound worst-case disk use
# against a malicious or misbehaving upstream repository/host.
MAX_DOWNLOAD_BYTES = 100 * 1024 * 1024
MAX_EXTRACTED_BYTES = 100 * 1024 * 1024


def load_entries():
    if not os.path.exists(REPO_FILE):
        return []
    try:
        with open(REPO_FILE) as f:
            data = json.load(f)
    except (OSError, ValueError):
        return []
    return [e for e in data.get('repositories', []) if e.get('category') in CATEGORY_DIRS]


def load_state():
    if not os.path.exists(STATE_FILE):
        return {}
    try:
        with open(STATE_FILE) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def save_state(state):
    tmp_path = STATE_FILE + '.tmp'
    with open(tmp_path, 'w') as f:
        json.dump(state, f)
    os.replace(tmp_path, STATE_FILE)


def entry_key(entry):
    return "{}|{}".format(entry['category'], entry['resolvedTarget'])


def dest_path(entry):
    category_dir = CATEGORY_DIRS[entry['category']]
    basename = os.path.basename(entry['sourcePath'])
    return os.path.join(CONFIG_DIR, category_dir, basename)


def copy_limited(src, dst, limit):
    copied = 0
    while True:
        chunk = src.read(65536)
        if not chunk:
            return
        copied += len(chunk)
        if copied > limit:
            raise RuntimeError('download exceeds {} byte limit'.format(limit))
        dst.write(chunk)


def safe_extractall(tar, path):
    abs_path = os.path.abspath(path)
    total = 0
    for member in tar.getmembers():
        member_path = os.path.abspath(os.path.join(path, member.name))
        if not (member_path == abs_path or member_path.startswith(abs_path + os.sep)):
            raise RuntimeError('unsafe path in tarball: ' + member.name)
        if member.isfile():
            total += member.size
            if total > MAX_EXTRACTED_BYTES:
                raise RuntimeError('extracted content exceeds {} byte limit'.format(MAX_EXTRACTED_BYTES))
    try:
        tar.extractall(path, filter='data')
    except TypeError:
        tar.extractall(path)


def fetch_and_place(entry):
    url = "{}/{}/tar.gz/{}".format(CODELOAD_BASE, entry['repository'], entry['ref'])
    with urllib.request.urlopen(url, timeout=30) as resp:
        with tempfile.TemporaryDirectory() as tmp:
            tar_path = os.path.join(tmp, 'repo.tar.gz')
            with open(tar_path, 'wb') as f:
                copy_limited(resp, f, MAX_DOWNLOAD_BYTES)
            extract_dir = os.path.join(tmp, 'extracted')
            os.makedirs(extract_dir, exist_ok=True)
            with tarfile.open(tar_path, 'r:gz') as tar:
                safe_extractall(tar, extract_dir)
            top_level = os.listdir(extract_dir)
            if len(top_level) != 1:
                raise RuntimeError('expected a single top-level directory, found {}'.format(len(top_level)))
            root = os.path.join(extract_dir, top_level[0])
            src = os.path.join(root, entry['sourcePath'])
            dest = dest_path(entry)
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            if os.path.isdir(dest):
                shutil.rmtree(dest)
            elif os.path.exists(dest):
                os.remove(dest)
            if os.path.isdir(src):
                shutil.copytree(src, dest)
            else:
                shutil.copy2(src, dest)


def remove_installed(entry_state):
    dest = entry_state.get('dest')
    if not dest:
        return
    try:
        if os.path.isdir(dest):
            shutil.rmtree(dest)
        elif os.path.exists(dest):
            os.remove(dest)
    except OSError as e:
        print('WARNING: failed to remove {}: {}'.format(dest, e), file=sys.stderr)


def sync_once(state):
    entries = load_entries()
    wanted = {entry_key(e): e for e in entries}

    changed = False

    for key in list(state.keys()):
        if key not in wanted:
            remove_installed(state[key])
            del state[key]
            changed = True
            print('removed {}'.format(key))

    for key, entry in wanted.items():
        current = state.get(key)
        if current is not None and current.get('ref') == entry['ref']:
            continue
        try:
            fetch_and_place(entry)
        except Exception as e:
            print('WARNING: failed to materialize {}: {}'.format(key, e), file=sys.stderr)
            continue
        state[key] = {'ref': entry['ref'], 'dest': dest_path(entry)}
        changed = True
        print('materialized {} from {}@{}'.format(key, entry['repository'], entry['ref']))

    if changed:
        save_state(state)


def main():
    state = load_state()
    while True:
        try:
            sync_once(state)
        except Exception as e:
            print('WARNING: sync cycle failed: {}'.format(e), file=sys.stderr)
        time.sleep(POLL_INTERVAL_SECONDS)


if __name__ == '__main__':
    main()
`

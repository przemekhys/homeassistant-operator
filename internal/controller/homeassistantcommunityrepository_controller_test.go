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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/communityrepo"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// buildFixtureTarball builds a gzip-compressed tarball wrapped in a single
// top-level "prefix/" directory, mirroring GitHub codeload's own layout — see
// internal/communityrepo.FetchTarball.
func buildFixtureTarball(prefix string, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: prefix + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, content := range files {
		full := prefix + "/" + name
		_ = tw.WriteHeader(&tar.Header{Name: full, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func fixtureFilesFor(category hav1alpha1.CommunityRepositoryCategory) map[string]string {
	switch category {
	case hav1alpha1.CategoryIntegration:
		return map[string]string{
			"hacs.json": `{"name":"Fixture Integration","category":"integration"}`,
			"custom_components/example_integration/manifest.json": `{"domain":"example_integration"}`,
			"custom_components/example_integration/__init__.py":   "",
		}
	case hav1alpha1.CategoryPlugin:
		return map[string]string{
			"hacs.json":       `{"name":"Fixture Card","category":"plugin","filename":"example-card.js"}`,
			"example-card.js": "console.log('fixture card');",
		}
	case hav1alpha1.CategoryTheme:
		return map[string]string{
			"hacs.json":                 `{"name":"Fixture Theme","category":"theme"}`,
			"themes/example_theme.yaml": "example_theme: {}\n",
		}
	case hav1alpha1.CategoryPythonScript:
		return map[string]string{
			"hacs.json":                        `{"name":"Fixture Script","category":"python_script"}`,
			"python_scripts/example_script.py": "logger.info('fixture')\n",
		}
	case hav1alpha1.CategoryTemplate:
		return map[string]string{
			"hacs.json": `{"name":"Fixture Template","category":"template"}`,
			"custom_templates/example_template.jinja": "{{ 1 + 1 }}",
		}
	}
	return nil
}

var _ = Describe("HomeAssistantCommunityRepository Controller", func() {
	const (
		namespace = "default"
	)

	var (
		reconciler      *HomeAssistantCommunityRepositoryReconciler
		codeloadServer  *httptest.Server
		haServer        *httptest.Server
		origCodeload    string
		origSettleDelay time.Duration
		upgrader        = websocket.Upgrader{}
	)

	reconcileRepo := func(name string) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
		})
	}

	getRepo := func(name string) *hav1alpha1.HomeAssistantCommunityRepository {
		repo := &hav1alpha1.HomeAssistantCommunityRepository{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, repo)).To(Succeed())
		return repo
	}

	createHA := func(name string) {
		ha := &hav1.HomeAssistant{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       hav1.HomeAssistantSpec{Version: "stable"},
		}
		Expect(k8sClient.Create(ctx, ha)).To(Succeed())
	}

	createAPIToken := func(haName string) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      haName + "-homeassistant-api-token",
				Namespace: namespace,
			},
			Data: map[string][]byte{"token": []byte("test-token")},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	}

	createRepo := func(name, haName string, category hav1alpha1.CommunityRepositoryCategory, repository string) {
		repo := &hav1alpha1.HomeAssistantCommunityRepository{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: hav1alpha1.HomeAssistantCommunityRepositorySpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: haName},
				Category:         category,
				Repository:       repository,
				Ref:              "v1.0.0",
			},
		}
		Expect(k8sClient.Create(ctx, repo)).To(Succeed())
	}

	registerFixture := func(
		fixtures map[string][]byte, repository string, category hav1alpha1.CommunityRepositoryCategory,
	) {
		const ref = "v1.0.0"
		path := fmt.Sprintf("/%s/tar.gz/%s", repository, ref)
		fixtures[path] = buildFixtureTarball(fmt.Sprintf("fixture-%s-%s", category, ref), fixtureFilesFor(category))
	}

	BeforeEach(func() {
		origCodeload = communityrepo.CodeloadBaseURL
		origSettleDelay = activationSettleDelay
		// envtest specs call Reconcile back-to-back with no real wall-clock delay,
		// so the production settle delay (waiting for ConfigMap-to-volume
		// propagation before the first activation attempt) would make every
		// hot-reload-category test hang on RequeueAfter forever.
		activationSettleDelay = 0

		reconciler = &HomeAssistantCommunityRepositoryReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(100),
		}
	})

	AfterEach(func() {
		communityrepo.CodeloadBaseURL = origCodeload
		activationSettleDelay = origSettleDelay
		if codeloadServer != nil {
			codeloadServer.Close()
			codeloadServer = nil
		}
		if haServer != nil {
			haServer.Close()
			haServer = nil
		}

		repoList := &hav1alpha1.HomeAssistantCommunityRepositoryList{}
		_ = k8sClient.List(ctx, repoList)
		for i := range repoList.Items {
			_ = k8sClient.Delete(ctx, &repoList.Items[i])
			_, _ = reconcileRepo(repoList.Items[i].Name)
			remaining := &hav1alpha1.HomeAssistantCommunityRepository{}
			key := client.ObjectKeyFromObject(&repoList.Items[i])
			if k8sClient.Get(ctx, key, remaining) == nil {
				controllerutil.RemoveFinalizer(remaining, communityRepositoryFinalizerName)
				_ = k8sClient.Update(ctx, remaining)
			}
		}
		Eventually(func() int {
			list := &hav1alpha1.HomeAssistantCommunityRepositoryList{}
			_ = k8sClient.List(ctx, list)
			return len(list.Items)
		}, time.Second*10, time.Millisecond*250).Should(Equal(0))

		cmList := &corev1.ConfigMapList{}
		_ = k8sClient.List(ctx, cmList, client.InNamespace(namespace))
		for i := range cmList.Items {
			_ = k8sClient.Delete(ctx, &cmList.Items[i])
		}

		secretList := &corev1.SecretList{}
		_ = k8sClient.List(ctx, secretList)
		for i := range secretList.Items {
			_ = k8sClient.Delete(ctx, &secretList.Items[i])
		}

		haList := &hav1.HomeAssistantList{}
		_ = k8sClient.List(ctx, haList)
		for i := range haList.Items {
			_ = k8sClient.Delete(ctx, &haList.Items[i])
		}
	})

	It("transitions Pending -> Validating -> Installing -> Installed for category integration", func() {
		fixtures := map[string][]byte{}
		registerFixture(fixtures, "acme/integration-fixture", hav1alpha1.CategoryIntegration)
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		createRepo("cr-integration", "ha-integration-missing", hav1alpha1.CategoryIntegration, "acme/integration-fixture")

		// Add finalizer.
		_, err := reconcileRepo("cr-integration")
		Expect(err).NotTo(HaveOccurred())

		// HomeAssistant does not exist yet -> Pending/HomeAssistantNotReady.
		_, err = reconcileRepo("cr-integration")
		Expect(err).NotTo(HaveOccurred())
		Expect(getRepo("cr-integration").Status.Phase).To(Equal(hav1alpha1.PhasePending))
		Expect(getRepo("cr-integration").Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoHomeAssistantNotReady)),
		))

		// Now create the HomeAssistant it references.
		createHA("ha-integration-missing")

		_, err = reconcileRepo("cr-integration") // -> Validating (persisted, transient)
		Expect(err).NotTo(HaveOccurred())
		Expect(getRepo("cr-integration").Status.Phase).To(Equal(hav1alpha1.PhaseValidating))

		_, err = reconcileRepo("cr-integration") // -> fetch+validate+conflict -> Installing
		Expect(err).NotTo(HaveOccurred())
		repo := getRepo("cr-integration")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalling))
		Expect(repo.Status.ResolvedTarget).To(Equal("example_integration"))

		result, err := reconcileRepo("cr-integration") // no pod acknowledgement yet -> remains Installing
		Expect(err).NotTo(HaveOccurred())
		repo = getRepo("cr-integration")
		Expect(result.RequeueAfter).To(Equal(integrationMaterializationPollInterval))
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalling))
		Expect(repo.Status.InstalledVersion).To(BeEmpty())
		Expect(repo.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoMaterializing)),
		))

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "ha-integration-missing-community-repositories", Namespace: namespace,
		}, cm)).To(Succeed())
		Expect(cm.Data["repositories.json"]).To(ContainSubstring("example_integration"))
		desiredHash := calculateIntegrationRepositoryHash(cm.Data[communityRepositoriesConfigMapKey])

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ha-integration-missing-0",
				Namespace: namespace,
				Annotations: map[string]string{
					communityRepositoryHashAnnotationKey: desiredHash,
				},
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: communityRepositoryInitContainerName, Image: "busybox"}},
				Containers:     []corev1.Container{{Name: "home-assistant", Image: "busybox"}},
			},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, pod) })

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(Succeed())
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name:  communityRepositoryInitContainerName,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1,
				Message:  "temporary DNS failure",
			}},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		_, err = reconcileRepo("cr-integration")
		Expect(err).NotTo(HaveOccurred())
		repo = getRepo("cr-integration")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalling))
		Expect(repo.Status.InstalledVersion).To(BeEmpty())
		Expect(repo.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoMaterializationFailed)),
		))
		Expect(repo.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Message }, ContainSubstring("temporary DNS failure")),
		))
		failedStatusVersion := repo.ResourceVersion

		result, err = reconcileRepo("cr-integration")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(integrationMaterializationPollInterval))
		Expect(getRepo("cr-integration").ResourceVersion).To(Equal(failedStatusVersion),
			"an unchanged materialization failure must not write status in a hot loop")

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(Succeed())
		pod.Status.Phase = corev1.PodRunning
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: communityRepositoryInitContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0,
				Message:  desiredHash,
			}},
		}}
		pod.Status.Conditions = []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionTrue,
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		_, err = reconcileRepo("cr-integration")
		Expect(err).NotTo(HaveOccurred())
		repo = getRepo("cr-integration")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))
		Expect(repo.Status.InstalledVersion).To(Equal("v1.0.0"))
	})

	DescribeTable("transitions Validating -> Installing -> Installed for hot-reload categories",
		func(category hav1alpha1.CommunityRepositoryCategory, expectedTarget string, servicePath string) {
			fixtures := map[string][]byte{}
			repository := fmt.Sprintf("acme/%s-fixture", category)
			registerFixture(fixtures, repository, category)
			codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tarball, ok := fixtures[r.URL.Path]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(tarball)
			}))
			communityrepo.CodeloadBaseURL = codeloadServer.URL

			if category == hav1alpha1.CategoryPlugin {
				haServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer func() { _ = conn.Close() }()
					_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
					var authMsg map[string]interface{}
					_ = conn.ReadJSON(&authMsg)
					_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})

					var cmd map[string]interface{}
					_ = conn.ReadJSON(&cmd)
					_ = conn.WriteJSON(map[string]interface{}{
						"id": cmd["id"], "type": "result", "success": true,
						"result": []map[string]interface{}{},
					})
				}))
			} else {
				haServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer GinkgoRecover()
					Expect(r.URL.Path).To(Equal(servicePath))
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[]`))
				}))
			}
			reconciler.NewHAClient = func(_ string) *haclient.Client {
				return haclient.NewClient(haServer.URL)
			}

			haName := strings.ReplaceAll(fmt.Sprintf("ha-%s", category), "_", "-")
			createHA(haName)
			createAPIToken(haName)

			name := strings.ReplaceAll(fmt.Sprintf("cr-%s", category), "_", "-")
			createRepo(name, haName, category, repository)

			_, err := reconcileRepo(name) // finalizer
			Expect(err).NotTo(HaveOccurred())
			_, err = reconcileRepo(name) // -> Validating (persisted)
			Expect(err).NotTo(HaveOccurred())
			Expect(getRepo(name).Status.Phase).To(Equal(hav1alpha1.PhaseValidating))
			_, err = reconcileRepo(name) // -> Installing
			Expect(err).NotTo(HaveOccurred())
			repo := getRepo(name)
			Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalling))
			Expect(repo.Status.ResolvedTarget).To(Equal(expectedTarget))
			_, err = reconcileRepo(name) // -> activation -> Installed
			Expect(err).NotTo(HaveOccurred())
			Expect(getRepo(name).Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))
		},
		Entry("theme", hav1alpha1.CategoryTheme, "example_theme", "/api/services/frontend/reload_themes"),
		Entry("python_script", hav1alpha1.CategoryPythonScript, "example_script", "/api/services/python_script/reload"),
		Entry("template", hav1alpha1.CategoryTemplate, "example_template",
			"/api/services/homeassistant/reload_custom_templates"),
		Entry("plugin", hav1alpha1.CategoryPlugin, "example-card", ""),
	)

	It("sets Failed/CategoryMismatch and never writes the ConfigMap", func() {
		fixtures := map[string][]byte{}
		path := "/acme/mismatch-fixture/tar.gz/v1.0.0"
		fixtures[path] = buildFixtureTarball("fixture-mismatch-v1.0.0", map[string]string{
			"hacs.json":                 `{"name":"Fixture Theme","category":"theme"}`,
			"themes/example_theme.yaml": "example_theme: {}\n",
		})
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		createHA("ha-mismatch")
		// Requested category is python_script, but the repo declares theme.
		createRepo("cr-mismatch", "ha-mismatch", hav1alpha1.CategoryPythonScript, "acme/mismatch-fixture")

		_, err := reconcileRepo("cr-mismatch") // finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-mismatch") // -> Validating (persisted)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-mismatch") // -> fetch+validate -> Failed
		Expect(err).NotTo(HaveOccurred())

		repo := getRepo("cr-mismatch")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseFailed))
		Expect(repo.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoCategoryMismatch)),
		))

		cm := &corev1.ConfigMap{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "ha-mismatch-community-repositories", Namespace: namespace}, cm)
		Expect(err).To(HaveOccurred()) // ConfigMap must not have been created
	})

	It("sets Failed/StructureInvalid for a repository missing the expected HACS layout", func() {
		fixtures := map[string][]byte{}
		path := "/acme/broken-fixture/tar.gz/v1.0.0"
		fixtures[path] = buildFixtureTarball("fixture-broken-v1.0.0", map[string]string{
			"README.md": "nothing relevant here",
		})
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		createHA("ha-broken")
		createRepo("cr-broken", "ha-broken", hav1alpha1.CategoryTheme, "acme/broken-fixture")

		_, err := reconcileRepo("cr-broken")
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-broken")
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-broken")
		Expect(err).NotTo(HaveOccurred())

		repo := getRepo("cr-broken")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseFailed))
		Expect(repo.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoStructureInvalid)),
		))
	})

	It("rejects a conflicting sibling with Failed/TargetConflict, leaving the first CR untouched", func() {
		fixtures := map[string][]byte{}
		registerFixture(fixtures, "acme/theme-fixture-a", hav1alpha1.CategoryTheme)
		registerFixture(fixtures, "acme/theme-fixture-b", hav1alpha1.CategoryTheme)
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		haServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		}))
		reconciler.NewHAClient = func(_ string) *haclient.Client {
			return haclient.NewClient(haServer.URL)
		}

		createHA("ha-conflict")
		createAPIToken("ha-conflict")

		createRepo("cr-conflict-a", "ha-conflict", hav1alpha1.CategoryTheme, "acme/theme-fixture-a")
		createRepo("cr-conflict-b", "ha-conflict", hav1alpha1.CategoryTheme, "acme/theme-fixture-b")

		// Drive the first CR all the way to Installed.
		for range []int{0, 1, 2, 3} {
			_, err := reconcileRepo("cr-conflict-a")
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(getRepo("cr-conflict-a").Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))

		// The second CR resolves to the same (category, resolvedTarget) — must conflict.
		_, err := reconcileRepo("cr-conflict-b") // finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-conflict-b") // -> Validating (persisted)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-conflict-b") // -> conflict -> Failed
		Expect(err).NotTo(HaveOccurred())

		repoB := getRepo("cr-conflict-b")
		Expect(repoB.Status.Phase).To(Equal(hav1alpha1.PhaseFailed))
		Expect(repoB.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoTargetConflict)),
		))

		// First CR must remain untouched (still Installed, owning the ConfigMap entry).
		Expect(getRepo("cr-conflict-a").Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))
		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "ha-conflict-community-repositories", Namespace: namespace,
		}, cm)).To(Succeed())
		Expect(cm.Data["repositories.json"]).To(ContainSubstring("acme/theme-fixture-a"))
		Expect(cm.Data["repositories.json"]).NotTo(ContainSubstring("acme/theme-fixture-b"))
	})

	It("sets Pending/HomeAssistantNotReady with a RequeueAfter when HomeAssistant is missing", func() {
		createRepo("cr-no-ha", "does-not-exist", hav1alpha1.CategoryTheme, "acme/some-fixture")

		_, err := reconcileRepo("cr-no-ha") // finalizer
		Expect(err).NotTo(HaveOccurred())
		result, err := reconcileRepo("cr-no-ha")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		repo := getRepo("cr-no-ha")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhasePending))
		Expect(repo.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoHomeAssistantNotReady)),
		))
	})

	It("keeps installedVersion at the old ref until a spec.ref update is confirmed", func() {
		fixtures := map[string][]byte{}
		fixtures["/acme/theme-update/tar.gz/v1.0.0"] = buildFixtureTarball("fixture-v1", map[string]string{
			"hacs.json":                 `{"name":"Fixture Theme","category":"theme"}`,
			"themes/example_theme.yaml": "example_theme:\n  version: 1\n",
		})
		fixtures["/acme/theme-update/tar.gz/v2.0.0"] = buildFixtureTarball("fixture-v2", map[string]string{
			"hacs.json":                 `{"name":"Fixture Theme","category":"theme"}`,
			"themes/example_theme.yaml": "example_theme:\n  version: 2\n",
		})
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		haServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(r.URL.Path).To(Equal("/api/services/frontend/reload_themes"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		}))
		reconciler.NewHAClient = func(_ string) *haclient.Client {
			return haclient.NewClient(haServer.URL)
		}

		createHA("ha-update")
		createAPIToken("ha-update")
		createRepo("cr-update", "ha-update", hav1alpha1.CategoryTheme, "acme/theme-update")

		_, err := reconcileRepo("cr-update") // finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-update") // -> Validating (persisted)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-update") // -> Installing
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-update") // -> Installed
		Expect(err).NotTo(HaveOccurred())

		repo := getRepo("cr-update")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))
		Expect(repo.Status.InstalledVersion).To(Equal("v1.0.0"))

		// Bump spec.ref — this must NOT immediately change installedVersion.
		repo.Spec.Ref = "v2.0.0"
		Expect(k8sClient.Update(ctx, repo)).To(Succeed())

		_, err = reconcileRepo("cr-update") // Installed(stale generation) -> Validating (persisted)
		Expect(err).NotTo(HaveOccurred())
		repo = getRepo("cr-update")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseValidating))
		Expect(repo.Status.InstalledVersion).To(Equal("v1.0.0"),
			"installedVersion must not change until the new ref is confirmed")

		_, err = reconcileRepo("cr-update") // -> Installing
		Expect(err).NotTo(HaveOccurred())
		repo = getRepo("cr-update")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalling))
		Expect(repo.Status.InstalledVersion).To(Equal("v1.0.0"),
			"installedVersion must not change until activation is confirmed")

		_, err = reconcileRepo("cr-update") // -> Installed (v2.0.0 confirmed)
		Expect(err).NotTo(HaveOccurred())
		repo = getRepo("cr-update")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))
		Expect(repo.Status.InstalledVersion).To(Equal("v2.0.0"))
	})

	It("leaves installedVersion untouched when spec.ref is updated to a non-existent ref", func() {
		fixtures := map[string][]byte{}
		fixtures["/acme/theme-badref/tar.gz/v1.0.0"] = buildFixtureTarball("fixture-v1", map[string]string{
			"hacs.json":                 `{"name":"Fixture Theme","category":"theme"}`,
			"themes/example_theme.yaml": "example_theme:\n  version: 1\n",
		})
		// No fixture registered for "v99.99.99" — the fake codeload server 404s it.
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		haServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		}))
		reconciler.NewHAClient = func(_ string) *haclient.Client {
			return haclient.NewClient(haServer.URL)
		}

		createHA("ha-badref")
		createAPIToken("ha-badref")
		createRepo("cr-badref", "ha-badref", hav1alpha1.CategoryTheme, "acme/theme-badref")

		for i := 0; i < 4; i++ {
			_, err := reconcileRepo("cr-badref")
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(getRepo("cr-badref").Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))
		Expect(getRepo("cr-badref").Status.InstalledVersion).To(Equal("v1.0.0"))

		repo := getRepo("cr-badref")
		repo.Spec.Ref = "v99.99.99"
		Expect(k8sClient.Update(ctx, repo)).To(Succeed())

		_, err := reconcileRepo("cr-badref") // -> Validating (persisted)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcileRepo("cr-badref") // -> fetch fails -> Failed
		Expect(err).NotTo(HaveOccurred())

		repo = getRepo("cr-badref")
		Expect(repo.Status.Phase).To(Equal(hav1alpha1.PhaseFailed))
		Expect(repo.Status.Conditions).To(ContainElement(
			WithTransform(func(c metav1.Condition) string { return c.Reason }, Equal(reasonRepoUnreachable)),
		))
		Expect(repo.Status.InstalledVersion).To(Equal("v1.0.0"),
			"the previously working installation must not be broken by a failed update")

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "ha-badref-community-repositories", Namespace: namespace,
		}, cm)).To(Succeed())
		Expect(cm.Data["repositories.json"]).To(ContainSubstring("v1.0.0"))
		Expect(cm.Data["repositories.json"]).NotTo(ContainSubstring("v99.99.99"))
	})

	It("removes the ConfigMap entry and the finalizer when an Installed resource is deleted", func() {
		fixtures := map[string][]byte{}
		registerFixture(fixtures, "acme/theme-delete", hav1alpha1.CategoryTheme)
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		haServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		}))
		reconciler.NewHAClient = func(_ string) *haclient.Client {
			return haclient.NewClient(haServer.URL)
		}

		createHA("ha-delete")
		createAPIToken("ha-delete")
		createRepo("cr-delete", "ha-delete", hav1alpha1.CategoryTheme, "acme/theme-delete")

		for i := 0; i < 4; i++ {
			_, err := reconcileRepo("cr-delete")
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(getRepo("cr-delete").Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "ha-delete-community-repositories", Namespace: namespace,
		}, cm)).To(Succeed())
		Expect(cm.Data["repositories.json"]).To(ContainSubstring("example_theme"))

		Expect(k8sClient.Delete(ctx, getRepo("cr-delete"))).To(Succeed())
		_, err := reconcileRepo("cr-delete")
		Expect(err).NotTo(HaveOccurred())

		err = k8sClient.Get(ctx, types.NamespacedName{Name: "cr-delete", Namespace: namespace},
			&hav1alpha1.HomeAssistantCommunityRepository{})
		Expect(err).To(HaveOccurred(), "the CR must be gone once the finalizer is removed")

		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "ha-delete-community-repositories", Namespace: namespace,
		}, cm)).To(Succeed())
		Expect(cm.Data["repositories.json"]).NotTo(ContainSubstring("example_theme"))
	})

	It("keeps an integration finalizer until the cleanup pod is Ready", func() {
		const haName = "ha-integration-delete"
		createHA(haName)
		createRepo("cr-integration-delete", haName, hav1alpha1.CategoryIntegration, "acme/integration-delete")
		_, err := reconcileRepo("cr-integration-delete") // finalizer
		Expect(err).NotTo(HaveOccurred())

		repo := getRepo("cr-integration-delete")
		repo.Status.Phase = hav1alpha1.PhaseInstalled
		repo.Status.ResolvedTarget = "example_integration"
		repo.Status.InstalledVersion = "v1.0.0"
		Expect(k8sClient.Status().Update(ctx, repo)).To(Succeed())

		oldContent := `{"repositories":[{"category":"integration","repository":"acme/integration-delete",` +
			`"ref":"v1.0.0","resolvedTarget":"example_integration",` +
			`"sourcePath":"custom_components/example_integration"}]}`
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: communityRepositoriesConfigMapName(haName), Namespace: namespace,
			},
			Data: map[string]string{communityRepositoriesConfigMapKey: oldContent},
		}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())

		labels := map[string]string{"app": haName}
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: haName, Namespace: namespace},
			Spec: appsv1.StatefulSetSpec{
				ServiceName: haName,
				Selector:    &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{
						communityRepositoryHashAnnotationKey: calculateIntegrationRepositoryHash(oldContent),
					}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "home-assistant", Image: "busybox"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, sts)).To(Succeed())

		Expect(k8sClient.Delete(ctx, getRepo("cr-integration-delete"))).To(Succeed())
		result, err := reconcileRepo("cr-integration-delete")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(integrationMaterializationPollInterval))
		Expect(getRepo("cr-integration-delete").Finalizers).To(ContainElement(communityRepositoryFinalizerName))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)).To(Succeed())
		cleanupHash := calculateIntegrationRepositoryHash(cm.Data[communityRepositoriesConfigMapKey])
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(Succeed())
		Expect(sts.Spec.Template.Annotations).To(HaveKeyWithValue(communityRepositoryHashAnnotationKey, cleanupHash))

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: haName + "-0", Namespace: namespace, Annotations: map[string]string{
				communityRepositoryHashAnnotationKey: cleanupHash,
			}},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: communityRepositoryInitContainerName, Image: "busybox"}},
				Containers:     []corev1.Container{{Name: "home-assistant", Image: "busybox"}},
			},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(Succeed())
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: communityRepositoryInitContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0, Message: cleanupHash,
			}},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		_, err = reconcileRepo("cr-integration-delete")
		Expect(err).NotTo(HaveOccurred())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "cr-integration-delete", Namespace: namespace},
			&hav1alpha1.HomeAssistantCommunityRepository{})
		Expect(err).To(HaveOccurred(), "the finalizer may be removed only after cleanup is confirmed")
	})

	It("removes the finalizer on deletion even when Home Assistant is unreachable (best-effort)", func() {
		fixtures := map[string][]byte{}
		registerFixture(fixtures, "acme/theme-unreachable-delete", hav1alpha1.CategoryTheme)
		codeloadServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tarball, ok := fixtures[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarball)
		}))
		communityrepo.CodeloadBaseURL = codeloadServer.URL

		haServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		}))
		reconciler.NewHAClient = func(_ string) *haclient.Client {
			return haclient.NewClient(haServer.URL)
		}

		createHA("ha-unreachable-delete")
		createAPIToken("ha-unreachable-delete")
		createRepo("cr-unreachable-delete", "ha-unreachable-delete", hav1alpha1.CategoryTheme,
			"acme/theme-unreachable-delete")

		for i := 0; i < 4; i++ {
			_, err := reconcileRepo("cr-unreachable-delete")
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(getRepo("cr-unreachable-delete").Status.Phase).To(Equal(hav1alpha1.PhaseInstalled))

		// Home Assistant becomes unreachable: the CR itself is gone (e.g. deleted or
		// never reconciled again). handleDeletion's getHomeAssistant lookup fails —
		// cleanup must still complete (best-effort) and the finalizer must still be
		// removed, matching the existing Automation/Scene/Script finalizer pattern.
		ha := &hav1.HomeAssistant{}
		haKey := types.NamespacedName{Name: "ha-unreachable-delete", Namespace: namespace}
		Expect(k8sClient.Get(ctx, haKey, ha)).To(Succeed())
		Expect(k8sClient.Delete(ctx, ha)).To(Succeed())

		Expect(k8sClient.Delete(ctx, getRepo("cr-unreachable-delete"))).To(Succeed())
		_, err := reconcileRepo("cr-unreachable-delete")
		Expect(err).NotTo(HaveOccurred(), "deletion must complete even when HomeAssistant is unreachable")

		err = k8sClient.Get(ctx, types.NamespacedName{Name: "cr-unreachable-delete", Namespace: namespace},
			&hav1alpha1.HomeAssistantCommunityRepository{})
		Expect(err).To(HaveOccurred(), "the finalizer must still be removed (best-effort) even though HA was unreachable")
	})
})

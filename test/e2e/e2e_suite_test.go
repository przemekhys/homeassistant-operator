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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/przemekhys/homeassistant-operator/test/utils"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	// - K3D_CLUSTER: Override k3d cluster name (defaults to homeassistant-operator-test-e2e)
	// - E2E_IMG: Override the operator image reference used by the suite (defaults to
	//   "example.com/homeassistant-operator:v0.0.1").
	// - E2E_SKIP_IMAGE_BUILD=true: Skip building/loading the operator image locally and
	//   use E2E_IMG as-is — for CI jobs that already loaded a pre-built image (e.g. via
	//   `k3d image import`) so each concurrent job doesn't redundantly rebuild it.
	// These variables are useful if CertManager is already installed, avoiding
	// re-installation and conflicts.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false

	// skipImageBuild skips the local `make docker-build` + k3d image load steps,
	// using projectImage as an already-loaded image instead.
	skipImageBuild = os.Getenv("E2E_SKIP_IMAGE_BUILD") == "true"

	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested. Override with E2E_IMG.
	projectImage = envOrDefault("E2E_IMG", "example.com/homeassistant-operator:v0.0.1")

	// tempKubeconfigPath holds the path to the isolated kubeconfig used by this test run.
	// It is deleted in SynchronizedAfterSuite.
	tempKubeconfigPath string
)

// envOrDefault returns the value of the named environment variable, or def if unset/empty.
func envOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary k3d cluster to validate project changes for use in CI jobs.
// The setup builds/loads the Manager Docker image locally and installs CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting homeassistant-operator integration test suite\n")
	RunSpecs(t, "e2e suite")
}

// SynchronizedBeforeSuite ensures setup runs only ONCE across all parallel processes
var _ = SynchronizedBeforeSuite(func() []byte {
	// This runs ONCE in process 1 only - do ALL setup here to avoid race conditions

	// SAFETY: Copy kubeconfig to a temp file and switch context inside the copy.
	// This isolates the test process from the user's ~/.kube/config so that:
	//   - the user can freely switch contexts in their terminal without affecting tests
	//   - tests never accidentally destroy a production cluster (e.g. kaczki-context)
	By(fmt.Sprintf("isolating kubeconfig for k3d test cluster %q", utils.K3dContextName()))
	var kubeconfigErr error
	tempKubeconfigPath, kubeconfigErr = utils.IsolateKubeconfigForK3d()
	ExpectWithOffset(1, kubeconfigErr).NotTo(HaveOccurred(),
		"Cannot isolate kubeconfig for k3d context %q — is the k3d cluster running? "+
			"Run: make k3d-create", utils.K3dContextName())

	if !skipImageBuild {
		By("building the manager(Operator) image")
		cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
		_, err := utils.Run(cmd)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager(Operator) image")

		By("loading the manager(Operator) image into k3d cluster")
		err = utils.LoadImageToK3dClusterWithName(projectImage)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager(Operator) image into k3d")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "E2E_SKIP_IMAGE_BUILD=true: using pre-loaded image %s\n", projectImage)
	}

	// The tests-e2e are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.
	// Setup CertManager before the suite if not skipped and if not already installed
	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	// Shared setup for all E2E tests: Install CRDs and deploy controller once
	By("installing CRDs")
	cmd := exec.Command("make", "install")
	output, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install CRDs")
	_, _ = fmt.Fprintf(GinkgoWriter, "CRD installation output:\n%s\n", output)

	// Verify that CRDs are actually installed in the cluster
	By("verifying CRDs are installed")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "crd", "homeassistants.ha.homeassistant.io")
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistant CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantconfigurations.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantConfiguration CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantsecrets.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantSecrets CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantbackups.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantBackup CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantrestores.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantRestore CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantautomations.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantAutomation CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantscenes.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantScene CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantscripts.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantScript CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantintegrations.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantIntegration CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantfloors.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantFloor CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantlabels.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantLabel CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantareas.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantArea CRD should exist")

		cmd = exec.Command("kubectl", "get", "crd", "homeassistantcommunityrepositories.ha.homeassistant.io")
		_, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HomeAssistantCommunityRepository CRD should exist")
	}, 30*time.Second, 2*time.Second).Should(Succeed(), "All CRDs should be installed and available")

	By("creating controller namespace")
	cmd = exec.Command("kubectl", "create", "ns", "homeassistant-operator-system")
	_, _ = utils.Run(cmd) // Ignore error if namespace already exists

	By("deploying the controller-manager")
	cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

	By("waiting for controller pod to be ready")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-l", "control-plane=controller-manager",
			"-o", "jsonpath={.items[0].status.phase}", "-n", "homeassistant-operator-system")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("Running"))
	}, utils.ControllerPodReadyTimeout, 5*time.Second).Should(Succeed())

	return nil // No data to share between processes
}, func(data []byte) {
	// This runs in ALL processes (including process 1) after process 1 finishes setup.
	// CRITICAL: each parallel process must isolate its own kubeconfig so that kubectl
	// commands within specs target k3d, not whatever context the user has in their shell.
	// Without this, specs running in process 2/3/4 would hit the wrong cluster.
	By(fmt.Sprintf("isolating kubeconfig for k3d test cluster %q (per-process)", utils.K3dContextName()))
	var err error
	tempKubeconfigPath, err = utils.IsolateKubeconfigForK3d()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(),
		"Cannot isolate kubeconfig for k3d context %q in worker process", utils.K3dContextName())
})

// SynchronizedAfterSuite ensures cleanup runs only ONCE after all parallel processes complete
var _ = SynchronizedAfterSuite(func() {
	// This runs in EACH process after its tests complete.
	// Clean up the per-process isolated kubeconfig temp file.
	if tempKubeconfigPath != "" {
		_ = os.Remove(tempKubeconfigPath)
	}
}, func() {
	// This runs ONCE in process 1 AFTER all processes complete
	// Do ALL cleanup here to avoid race conditions

	By("undeploying the controller-manager")
	cmd := exec.Command("make", "undeploy")
	_, _ = utils.Run(cmd)

	By("deleting controller namespace")
	cmd = exec.Command("kubectl", "delete", "ns", "homeassistant-operator-system", "--ignore-not-found=true")
	_, _ = utils.Run(cmd)

	By("uninstalling CRDs")
	cmd = exec.Command("make", "uninstall")
	_, _ = utils.Run(cmd)

	// Teardown CertManager after the suite if not skipped and if it was not already installed
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}

})

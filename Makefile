# VERSION defines the project version for the bundle.
# Update this value when you upgrade the version of your project.
# To re-generate a bundle for another specific version without changing the standard setup, you can:
# - use the VERSION as arg of the bundle target (e.g make bundle VERSION=0.0.2)
# - use environment variables to overwrite this value (e.g export VERSION=0.0.2)
VERSION ?= 0.9.0
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# CHANNELS define the bundle channels used in the bundle.
# Add a new line here if you would like to change its default config. (E.g CHANNELS = "candidate,fast,stable")
# To re-generate a bundle for other specific channels without changing the standard setup, you can:
# - use the CHANNELS as arg of the bundle target (e.g make bundle CHANNELS=candidate,fast,stable)
# - use environment variables to overwrite this value (e.g export CHANNELS="candidate,fast,stable")
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif

# DEFAULT_CHANNEL defines the default channel used in the bundle.
# Add a new line here if you would like to change its default config. (E.g DEFAULT_CHANNEL = "stable")
# To re-generate a bundle for any other default channel without changing the default setup, you can:
# - use the DEFAULT_CHANNEL as arg of the bundle target (e.g make bundle DEFAULT_CHANNEL=stable)
# - use environment variables to overwrite this value (e.g export DEFAULT_CHANNEL="stable")
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# IMAGE_TAG_BASE defines the container registry namespace and part of the image name for remote images.
# This variable is used to construct full image tags for bundle and catalog images.
#
# For example, running 'make bundle-build bundle-push catalog-build catalog-push' will build and push both
# ghcr.io/przemekhys/homeassistant-operator-bundle:$VERSION and ghcr.io/przemekhys/homeassistant-operator-catalog:$VERSION.
IMAGE_TAG_BASE ?= ghcr.io/przemekhys/homeassistant-operator

# BUNDLE_IMG defines the image:tag used for the bundle.
# You can use it as an arg. (E.g make bundle-build BUNDLE_IMG=<some-registry>/<project-name-bundle>:<tag>)
BUNDLE_IMG ?= $(IMAGE_TAG_BASE)-bundle:v$(VERSION)

# BUNDLE_GEN_FLAGS are the flags passed to the operator-sdk generate bundle command
BUNDLE_GEN_FLAGS ?= -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)

# USE_IMAGE_DIGESTS defines if images are resolved via tags or digests
# You can enable this value if you would like to use SHA Based Digests
# To enable set flag to true
USE_IMAGE_DIGESTS ?= false
ifeq ($(USE_IMAGE_DIGESTS), true)
	BUNDLE_GEN_FLAGS += --use-image-digests
endif

# Set the Operator SDK version to use. By default, what is installed on the system is used.
# This is useful for CI or a project to utilize a specific version of the operator-sdk toolkit.
OPERATOR_SDK_VERSION ?= unknown
# Image URL to use all building/pushing image targets
IMG ?= ghcr.io/przemekhys/homeassistant-operator:v$(VERSION)

# LDFLAGS for injecting version information
LDFLAGS ?= -X github.com/przemekhys/homeassistant-operator/internal/version.Version=$(VERSION) \
           -X github.com/przemekhys/homeassistant-operator/internal/version.GitCommit=$(GIT_COMMIT)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run unit tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# E2E tests use k3d (recommended for k3s-like target environments)
K3D_CLUSTER_E2E ?= homeassistant-operator-test-e2e
K3D_MEMORY_E2E ?= 12g
# renovate: datasource=docker depName=rancher/k3s
K3S_VERSION ?= v1.36.4-k3s1
# renovate: datasource=docker depName=ghcr.io/home-assistant/home-assistant
HA_VERSION ?= 2026.9.0

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a k3d cluster for e2e tests (always creates fresh cluster)
	@command -v k3d >/dev/null 2>&1 || { echo "k3d is not installed. Install with: curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash"; exit 1; }
	@echo "Ensuring clean k3d cluster state..."
	@k3d cluster delete $(K3D_CLUSTER_E2E) 2>/dev/null || true
	@echo "Creating fresh k3d cluster $(K3D_CLUSTER_E2E) with $(K3D_MEMORY_E2E) memory (k3s $(K3S_VERSION))..."
	@k3d cluster create $(K3D_CLUSTER_E2E) --image rancher/k3s:$(K3S_VERSION) --agents 0 --servers-memory $(K3D_MEMORY_E2E)
	@echo "Ensuring Home Assistant image $(HA_VERSION) is in local Docker cache..."
	@docker image inspect ghcr.io/home-assistant/home-assistant:$(HA_VERSION) >/dev/null 2>&1 || \
		docker pull ghcr.io/home-assistant/home-assistant:$(HA_VERSION)
	@echo "Importing Home Assistant image from local Docker cache into k3d containerd..."
	@k3d image import ghcr.io/home-assistant/home-assistant:$(HA_VERSION) -c $(K3D_CLUSTER_E2E)

.PHONY: test-e2e
test-e2e: manifests generate fmt vet ginkgo ## Run all E2E tests sequentially (1 bootstrap)
	@echo "Running all E2E tests (1 bootstrap, sequential)..."
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v \
		--timeout=60m \
		./test/e2e/ | tee test-e2e.log

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the k3d cluster used for e2e tests
	@echo "Cleaning up k3d cluster $(K3D_CLUSTER_E2E)..."
	@k3d cluster delete $(K3D_CLUSTER_E2E) 2>/dev/null || true

# Local, single-job reproductions of the seven concurrent CI jobs defined in
# .github/workflows/test-e2e-parallel.yml. See docs/development/testing.md
# for what each label-filter subset covers.
#
# setup-test-e2e is invoked from within the recipe (not as a prerequisite) so
# that validation prerequisites (manifests/generate/fmt/vet/ginkgo) run first,
# and an EXIT/INT/TERM trap guarantees cleanup-test-e2e always runs afterward
# — including when setup itself, a validation step, or the Ginkgo run fails
# (this Makefile's `bash -o pipefail`/`-e` settings mean a failing pipeline
# would otherwise skip straight past an un-trapped cleanup call).

.PHONY: test-e2e-critical-a
test-e2e-critical-a: manifests generate fmt vet ginkgo ## Run the critical-path group-a e2e job locally
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	CERT_MANAGER_INSTALL_SKIP=true K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v --label-filter="critical-path && group-a" --timeout=8m ./test/e2e/ | tee test-e2e.log

.PHONY: test-e2e-critical-b
test-e2e-critical-b: manifests generate fmt vet ginkgo ## Run the critical-path group-b e2e job locally
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	CERT_MANAGER_INSTALL_SKIP=true K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v --label-filter="critical-path && group-b" --timeout=7m ./test/e2e/ | tee test-e2e.log

.PHONY: test-e2e-tls
test-e2e-tls: manifests generate fmt vet ginkgo ## Run the tls e2e job locally (installs cert-manager)
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v --label-filter=tls --timeout=7m ./test/e2e/ | tee test-e2e.log

.PHONY: test-e2e-network-policy
test-e2e-network-policy: manifests generate fmt vet ginkgo ## Run the network-policy e2e job locally
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	CERT_MANAGER_INSTALL_SKIP=true K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v --label-filter=network-policy --timeout=8m ./test/e2e/ | tee test-e2e.log

.PHONY: test-e2e-pod-security
test-e2e-pod-security: manifests generate fmt vet ginkgo ## Run the pod-security e2e job locally
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	CERT_MANAGER_INSTALL_SKIP=true K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v --label-filter=pod-security --timeout=8m ./test/e2e/ | tee test-e2e.log

.PHONY: test-e2e-community-repository-a
test-e2e-community-repository-a: manifests generate fmt vet ginkgo ## Run the community-repository group-a e2e job locally
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	CERT_MANAGER_INSTALL_SKIP=true K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v --label-filter="community-repository && group-a" --timeout=9m ./test/e2e/ | tee test-e2e.log

.PHONY: test-e2e-community-repository-b
test-e2e-community-repository-b: manifests generate fmt vet ginkgo ## Run the community-repository group-b e2e job locally
	trap '$(MAKE) cleanup-test-e2e' EXIT INT TERM; \
	$(MAKE) setup-test-e2e; \
	CERT_MANAGER_INSTALL_SKIP=true K3D_CLUSTER=$(K3D_CLUSTER_E2E) $(GINKGO) run \
		-v --label-filter="community-repository && group-b" --timeout=10m ./test/e2e/ | tee test-e2e.log

##@ k3d Testing (recommended for k3s target environments)

K3D_CLUSTER ?= ha-operator-test
K3D_MEMORY ?= 12g

.PHONY: k3d-create
k3d-create: ## Create a k3d cluster for testing
	@command -v k3d >/dev/null 2>&1 || { echo "k3d is not installed. Install with: curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash"; exit 1; }
	@k3d cluster list | grep -q $(K3D_CLUSTER) && echo "Cluster $(K3D_CLUSTER) already exists" || k3d cluster create $(K3D_CLUSTER) --image rancher/k3s:$(K3S_VERSION) --agents 0 --servers-memory $(K3D_MEMORY)

.PHONY: k3d-delete
k3d-delete: ## Delete the k3d test cluster
	k3d cluster delete $(K3D_CLUSTER)

.PHONY: k3d-load
k3d-load: docker-build ## Build and load the operator image into k3d
	k3d image import ${IMG} -c $(K3D_CLUSTER)

.PHONY: test-k3d
test-k3d: k3d-create k3d-load install deploy ## Full test cycle on k3d: create cluster, load image, install CRDs, deploy operator
	@echo "Operator deployed to k3d cluster $(K3D_CLUSTER)"
	@echo "Run 'kubectl apply -f config/samples/' to create a HomeAssistant instance"

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter (clears cache for fresh analysis)
	$(GOLANGCI_LINT) cache clean
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes (clears cache for fresh analysis)
	$(GOLANGCI_LINT) cache clean
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

##@ Documentation

DOCS_VENV ?= .venv

.PHONY: docs-setup
docs-setup: ## Create Python venv and install MkDocs dependencies
	@# The stamp records which manifest the venv was built from. On a change the
	@# venv is rebuilt rather than topped up: pip never uninstalls what the
	@# manifest dropped, so a reused environment would keep diverging from it.
	@# Without the stamp, adding a plugin failed later with an unknown-plugin
	@# error in an environment that looked already set up.
	@want="$$(sha256sum docs/requirements.txt | cut -d' ' -f1)"; \
	have="$$(cat $(DOCS_VENV)/.installed 2>/dev/null || true)"; \
	if [ "$$want" != "$$have" ]; then \
		rm -rf $(DOCS_VENV); \
		python3 -m venv $(DOCS_VENV); \
		$(DOCS_VENV)/bin/pip install -r docs/requirements.txt -q; \
		echo "$$want" > $(DOCS_VENV)/.installed; \
	fi

.PHONY: docs-serve
docs-serve: docs-api ## Serve documentation locally (http://127.0.0.1:8000)
	@$(MAKE) docs-setup
	$(DOCS_VENV)/bin/mkdocs serve --strict

.PHONY: docs-build
docs-build: docs-api ## Build documentation to site/ (regenerates API reference first)
	@$(MAKE) docs-setup
	$(DOCS_VENV)/bin/mkdocs build --strict

.PHONY: docs-verify
docs-verify: docs-build ## Pre-PR gate for docs: strict build (links + anchors), self-contained check, pasteable shell snippets
	./hack/verify-docs-selfcontained.sh
	./hack/verify-docs-shell.sh

.PHONY: docs-api
docs-api: crd-ref-docs ## Regenerate docs/reference/api.md from Go types
	$(CRD_REF_DOCS) \
		--source-path=./api \
		--config=./docs/crd-ref-docs.yaml \
		--renderer=markdown \
		--output-path=./docs/reference/api.md
	@# Stamp in the type line every published page carries — crd-ref-docs has no
	@# hook for this, and the file is regenerated on every publish — then trim the
	@# trailing blank lines the generator emits. pre-commit's end-of-file-fixer
	@# strips them on the way in, so without this the freshly generated file never
	@# matches the committed one and every run reports drift.
	@{ head -1 ./docs/reference/api.md; echo; \
	   echo '*Reference — every field of every custom resource, generated from the Go types. Look things up here; it does not teach.*'; \
	   tail -n +2 ./docs/reference/api.md; } > ./docs/reference/api.md.tmp
	@printf '%s\n' "$$(cat ./docs/reference/api.md.tmp)" > ./docs/reference/api.md
	@rm -f ./docs/reference/api.md.tmp

##@ Security

.PHONY: security-check
security-check: ## Run govulncheck to scan for vulnerabilities
	@echo "Running govulncheck..."
	@go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: verify-pss
verify-pss: kustomize ## Verify operator manifests satisfy the "restricted" Pod Security Standard
	@KUSTOMIZE=$(KUSTOMIZE) ./hack/verify-pss.sh

# Severity levels reported by security-scan-manifests. Report-only for now.
TRIVY_SEVERITY ?= MEDIUM,HIGH,CRITICAL

.PHONY: security-scan-manifests
security-scan-manifests: kustomize ## Scan rendered Kustomize + Helm manifests for misconfigurations (report-only)
	@command -v $(TRIVY) >/dev/null 2>&1 || { echo "❌ trivy not found — install from https://github.com/aquasecurity/trivy"; exit 1; }
	@echo "==> Scanning Kustomize render (config/default)"
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
		$(KUSTOMIZE) build config/default > "$$tmp/manifests.yaml"; \
		$(TRIVY) config --exit-code 0 --severity $(TRIVY_SEVERITY) "$$tmp"
	@echo "==> Scanning Helm chart (charts/homeassistant-operator, default values)"
	@$(TRIVY) config --exit-code 0 --severity $(TRIVY_SEVERITY) \
		--skip-dirs charts/homeassistant-operator/crds \
		--skip-dirs charts/homeassistant-operator/tests \
		charts/homeassistant-operator

.PHONY: dupl-check
dupl-check: ## Check for duplicate code (excluding tests and generated files)
	@echo "Checking for code duplication..."
	@golangci-lint run --enable-only=dupl

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -ldflags "$(LDFLAGS)" -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run -ldflags "$(LDFLAGS)" ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build --build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name homeassistant-operator-builder
	$(CONTAINER_TOOL) buildx use homeassistant-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm homeassistant-operator-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	output="$$(pwd)/dist/install.yaml"; tmpdir=$$(mktemp -d); trap 'rm -rf "$$tmpdir"' EXIT; \
	cp -R config "$$tmpdir/config"; \
	cd "$$tmpdir/config/manager" && $(KUSTOMIZE) edit set image controller=${IMG}; \
	$(KUSTOMIZE) build "$$tmpdir/config/default" > "$$output"
	grep -Fqx '        image: ${IMG}' dist/install.yaml
	grep -qx 'kind: CustomResourceDefinition' dist/install.yaml
	grep -qx 'kind: Deployment' dist/install.yaml

##@ Helm

HELM_CHART_DIR ?= charts/homeassistant-operator
HELM_REGISTRY ?= $(shell echo $(IMAGE_TAG_BASE) | rev | cut -d/ -f2- | rev)

## Helm dev/CI tooling (never a runtime dependency of the chart or operator)
HELM_DOCS ?= $(LOCALBIN)/helm-docs
KUBECONFORM ?= $(LOCALBIN)/kubeconform
# renovate: datasource=github-releases depName=norwoodj/helm-docs
HELM_DOCS_VERSION ?= v1.14.2
# renovate: datasource=github-releases depName=yannh/kubeconform
KUBECONFORM_VERSION ?= v0.8.0
# renovate: datasource=github-releases depName=helm-unittest/helm-unittest
HELM_UNITTEST_VERSION ?= v1.1.2

.PHONY: helm-tools
helm-tools: helm-docs-bin kubeconform helm-unittest-plugin ## Install Helm dev/CI tooling (helm-docs, kubeconform, helm-unittest plugin).

.PHONY: helm-docs-bin
helm-docs-bin: $(HELM_DOCS) ## Download helm-docs locally if necessary.
$(HELM_DOCS): $(LOCALBIN)
	$(call go-install-tool,$(HELM_DOCS),github.com/norwoodj/helm-docs/cmd/helm-docs,$(HELM_DOCS_VERSION))

.PHONY: kubeconform
kubeconform: $(KUBECONFORM) ## Download kubeconform locally if necessary.
$(KUBECONFORM): $(LOCALBIN)
	$(call go-install-tool,$(KUBECONFORM),github.com/yannh/kubeconform/cmd/kubeconform,$(KUBECONFORM_VERSION))

.PHONY: helm-unittest-plugin
helm-unittest-plugin: ## Install/verify the helm-unittest plugin (pinned to HELM_UNITTEST_VERSION).
	@want="$(HELM_UNITTEST_VERSION)"; want="$${want#v}"; \
	have="$$(helm plugin list 2>/dev/null | awk '$$1=="unittest"{print $$2}')"; \
	if [ "$$have" = "$$want" ]; then \
		echo "helm-unittest $$want already installed"; \
	else \
		[ -n "$$have" ] && helm plugin uninstall unittest >/dev/null 2>&1 || true; \
		helm plugin install https://github.com/helm-unittest/helm-unittest --version $(HELM_UNITTEST_VERSION) --verify=false 2>/dev/null || \
		helm plugin install https://github.com/helm-unittest/helm-unittest --version $(HELM_UNITTEST_VERSION); \
	fi

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart.
	helm lint $(HELM_CHART_DIR)

## --- Single source of truth: generate chart's static parts from config/ ----------

.PHONY: helm-sync
helm-sync: kustomize ## Regenerate chart CRDs + RBAC rules from the authoritative config/ (mutates the chart).
	@KUSTOMIZE=$(KUSTOMIZE) HELM_CHART_DIR=$(HELM_CHART_DIR) ./hack/sync-chart-from-config.sh

.PHONY: helm-verify-sync
helm-verify-sync: kustomize ## Fail if the committed chart drifted from config/ (run `make helm-sync`).
	@KUSTOMIZE=$(KUSTOMIZE) HELM_CHART_DIR=$(HELM_CHART_DIR) ./hack/verify-chart-sync.sh

.PHONY: helm-verify-equivalence
helm-verify-equivalence: kustomize ## Fail if Kustomize and Helm renders diverge on RBAC/securityContext/image/PSA.
	@KUSTOMIZE=$(KUSTOMIZE) HELM_CHART_DIR=$(HELM_CHART_DIR) ./hack/verify-equivalence.sh

.PHONY: helm-verify-rbac-upgrade
helm-verify-rbac-upgrade: kustomize ## Fail if the chart expands RBAC vs the previous release without justification.
	@KUSTOMIZE=$(KUSTOMIZE) HELM_CHART_DIR=$(HELM_CHART_DIR) HELM_REGISTRY=$(HELM_REGISTRY) ./hack/verify-rbac-upgrade.sh

.PHONY: verify-network-policy
verify-network-policy: kustomize ## Fail if the operator's own metrics/webhook NetworkPolicy rules are missing/malformed in Kustomize or Helm.
	@KUSTOMIZE=$(KUSTOMIZE) HELM_CHART_DIR=$(HELM_CHART_DIR) ./hack/verify-network-policy.sh

## --- Chart quality gates ---------------------------------------------------------

.PHONY: helm-schema-lint
helm-schema-lint: ## Lint the chart and validate default + example values against values.schema.json.
	helm lint $(HELM_CHART_DIR)
	helm template schema-check $(HELM_CHART_DIR) >/dev/null
	helm template schema-check $(HELM_CHART_DIR) --set replicaCount=2 --set image.pullPolicy=Always >/dev/null

.PHONY: helm-unittest
helm-unittest: helm-unittest-plugin ## Run helm-unittest template unit tests.
	helm unittest $(HELM_CHART_DIR)

.PHONY: helm-docs
helm-docs: helm-docs-bin ## Generate the chart values documentation (README.md) from values.yaml.
	$(HELM_DOCS) --chart-search-root=$(HELM_CHART_DIR) --skip-version-footer

.PHONY: helm-verify-docs
helm-verify-docs: helm-docs-bin ## Fail if the committed chart README drifted from values.yaml (run `make helm-docs`).
	@HELM_DOCS=$(HELM_DOCS) HELM_CHART_DIR=$(HELM_CHART_DIR) ./hack/verify-helm-docs.sh

.PHONY: helm-verify
helm-verify: helm-verify-sync helm-verify-equivalence helm-verify-rbac-upgrade verify-network-policy helm-schema-lint helm-unittest helm-verify-docs ## Fast pre-PR gate: all chart checks that do not need a cluster.

## --- End-to-end / packaging ------------------------------------------------------

.PHONY: helm-test-e2e
helm-test-e2e: ## Fresh install + upgrade N-1 -> HEAD on k3d (requires k3d + docker).
	@HELM_CHART_DIR=$(HELM_CHART_DIR) HELM_REGISTRY=$(HELM_REGISTRY) IMG=$(IMG) ./hack/helm-e2e.sh

.PHONY: helm-smoke-oci
helm-smoke-oci: ## Install the published OCI chart artifact on k3d (post-publish smoke test).
	@HELM_REGISTRY=$(HELM_REGISTRY) ./hack/helm-smoke-oci.sh

.PHONY: helm-package
helm-package: ## Package the Helm chart into a .tgz archive.
	mkdir -p dist
	helm package $(HELM_CHART_DIR) --destination dist/

.PHONY: helm-push
helm-push: helm-package ## Push Helm chart to OCI registry (matches release.yml publish path).
	helm push dist/homeassistant-operator-$(VERSION).tgz oci://$(HELM_REGISTRY)/charts

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GINKGO ?= $(LOCALBIN)/ginkgo
CRD_REF_DOCS ?= $(LOCALBIN)/crd-ref-docs
TRIVY ?= trivy

## Tool Versions
# kustomize tags this module kustomize/vX.Y.Z (monorepo), which github-releases
# can't resolve against the unprefixed vX.Y.Z below — datasource=go instead
# resolves through the module path itself, exactly as `go list -m` already does.
# renovate: datasource=go depName=sigs.k8s.io/kustomize/kustomize/v5
KUSTOMIZE_VERSION ?= v5.8.1
# renovate: datasource=github-releases depName=kubernetes-sigs/controller-tools
CONTROLLER_TOOLS_VERSION ?= v0.22.0
# renovate: datasource=github-releases depName=elastic/crd-ref-docs
CRD_REF_DOCS_VERSION ?= v0.3.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.13.2
#GINKGO_VERSION is derived from go.mod (not a hardcoded/renovate-tracked value) so the
#CLI installed by `make ginkgo` can never drift from the github.com/onsi/ginkgo/v2
#package version imported by the test code — a mismatch produces a noisy but harmless
#warning at best, and a real compatibility issue at worst.
GINKGO_VERSION ?= $(shell go list -m -f "{{ .Version }}" github.com/onsi/ginkgo/v2)

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: ginkgo
ginkgo: $(GINKGO) ## Download ginkgo CLI locally if necessary.
$(GINKGO): $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

.PHONY: crd-ref-docs
crd-ref-docs: $(CRD_REF_DOCS) ## Download crd-ref-docs locally if necessary.
$(CRD_REF_DOCS): $(LOCALBIN)
	$(call go-install-tool,$(CRD_REF_DOCS),github.com/elastic/crd-ref-docs,$(CRD_REF_DOCS_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef

.PHONY: operator-sdk
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk
operator-sdk: ## Download operator-sdk locally if necessary.
ifeq (,$(wildcard $(OPERATOR_SDK)))
ifeq (, $(shell which operator-sdk 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPERATOR_SDK)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPERATOR_SDK) https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$${OS}_$${ARCH} ;\
	chmod +x $(OPERATOR_SDK) ;\
	}
else
OPERATOR_SDK = $(shell which operator-sdk)
endif
endif

.PHONY: bundle
bundle: manifests kustomize operator-sdk ## Generate bundle manifests and metadata, then validate generated files.
	$(OPERATOR_SDK) generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle $(BUNDLE_GEN_FLAGS)
	$(OPERATOR_SDK) bundle validate ./bundle

.PHONY: bundle-build
bundle-build: ## Build the bundle image.
	$(CONTAINER_TOOL) build -f bundle.Dockerfile -t $(BUNDLE_IMG) .

.PHONY: bundle-push
bundle-push: ## Push the bundle image.
	$(MAKE) docker-push IMG=$(BUNDLE_IMG)

.PHONY: opm
OPM = $(LOCALBIN)/opm
opm: ## Download opm locally if necessary.
ifeq (,$(wildcard $(OPM)))
ifeq (,$(shell which opm 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPM)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPM) https://github.com/operator-framework/operator-registry/releases/download/v1.55.0/$${OS}-$${ARCH}-opm ;\
	chmod +x $(OPM) ;\
	}
else
OPM = $(shell which opm)
endif
endif

# A comma-separated list of bundle images (e.g. make catalog-build BUNDLE_IMGS=example.com/operator-bundle:v0.1.0,example.com/operator-bundle:v0.2.0).
# These images MUST exist in a registry and be pull-able.
BUNDLE_IMGS ?= $(BUNDLE_IMG)

# The image tag given to the resulting catalog image (e.g. make catalog-build CATALOG_IMG=example.com/operator-catalog:v0.2.0).
CATALOG_IMG ?= $(IMAGE_TAG_BASE)-catalog:v$(VERSION)

# Set CATALOG_BASE_IMG to an existing catalog image tag to add $BUNDLE_IMGS to that image.
ifneq ($(origin CATALOG_BASE_IMG), undefined)
FROM_INDEX_OPT := --from-index $(CATALOG_BASE_IMG)
endif

# Build a catalog image by adding bundle images to an empty catalog using the operator package manager tool, 'opm'.
# This recipe invokes 'opm' in 'semver' bundle add mode. For more information on add modes, see:
# https://github.com/operator-framework/community-operators/blob/7f1438c/docs/packaging-operator.md#updating-your-existing-operator
.PHONY: catalog-build
catalog-build: opm ## Build a catalog image.
	$(OPM) index add --container-tool $(CONTAINER_TOOL) --mode semver --tag $(CATALOG_IMG) --bundles $(BUNDLE_IMGS) $(FROM_INDEX_OPT)

# Push the catalog image.
.PHONY: catalog-push
catalog-push: ## Push a catalog image.
	$(MAKE) docker-push IMG=$(CATALOG_IMG)

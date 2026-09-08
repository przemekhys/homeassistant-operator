# Home Assistant Operator

[![Lint](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml)
[![Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml)
[![E2E Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml)
[![Security Scan](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/przemekhys/homeassistant-operator/badge)](https://scorecard.dev/viewer/?uri=github.com/przemekhys/homeassistant-operator)

A Kubernetes operator for deploying and managing [Home Assistant](https://www.home-assistant.io/) instances with declarative configuration, zero-touch bootstrap, and GitOps-ready automations.

**[Full documentation →](https://przemekhys.github.io/homeassistant-operator/)**

Release artifacts (container image, Helm chart) are keylessly signed — see
[Signed Releases](https://przemekhys.github.io/homeassistant-operator/how-to/verify-signed-releases/)
to verify them or enforce verification in your cluster with Kyverno.

## Quick Start

```sh
helm install homeassistant-operator oci://ghcr.io/przemekhys/charts/homeassistant-operator \
  --namespace homeassistant-operator-system \
  --create-namespace \
  --set 'watchNamespaces={homeassistant}'
```

`watchNamespaces` lists the namespaces holding your Home Assistant resources.
Omitting it gives the operator cluster-wide permissions, which is deprecated.

See the [installation guide](https://przemekhys.github.io/homeassistant-operator/how-to/install-operator/) for a full walkthrough, including the plain-manifest (kustomize) alternative.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) or the [contributing guide](https://przemekhys.github.io/homeassistant-operator/development/contributing/).

## Acknowledgments

- [Home Assistant](https://www.home-assistant.io/) - The amazing home automation platform
- [Operator SDK](https://sdk.operatorframework.io/) - Framework for building Kubernetes operators
- [Kubebuilder](https://book.kubebuilder.io/) - SDK for building Kubernetes APIs
- [cert-controller](https://github.com/open-policy-agent/cert-controller) (OPA) - Self-managed rotation of the webhook serving certificate, so the operator needs no cert-manager

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.

# Home Assistant Operator

[![Lint](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml)
[![Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml)
[![E2E Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml)
[![Security Scan](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/przemekhys/homeassistant-operator/badge)](https://scorecard.dev/viewer/?uri=github.com/przemekhys/homeassistant-operator)

**Manage Home Assistant like the rest of your Kubernetes homelab.**

Home Assistant Operator lets you manage [Home Assistant](https://www.home-assistant.io/) through the Kubernetes API and tools you already use. Declare the configuration, integrations, and automations you want the operator to manage, and keep using the Home Assistant UI for everything else. Apply resources directly with `kubectl`, or connect your GitOps workflow.

[Create your first instance →](tutorials/first-instance.md){ .md-button .md-button--primary }
[Explore the guides →](how-to/index.md){ .md-button }

## Your smart home, familiar tools

- **Use one way to manage your homelab.** Manage selected parts of Home Assistant as Kubernetes resources, alongside the rest of your infrastructure.
- **Manage more than a running container.** Declare automations, scenes, scripts, integrations, and the floors, areas, and labels that describe your home.
- **Skip the manual setup of a new instance.** The operator can complete onboarding, create the initial admin account, and prepare API access for the resources it manages.
- **Choose what to manage declaratively.** Keep a stable base in Kubernetes resources and use the Home Assistant UI for day-to-day control, experiments, and anything you prefer to manage there.

## More than a Home Assistant deployment

If you already run Home Assistant with Helm or your own manifests, you know how to keep it running on Kubernetes. This operator extends that approach to what happens **inside Home Assistant**.

Apply your declarations with `kubectl`; the operator applies them to Home Assistant and keeps the parts it manages aligned with those declarations. Supported configuration changes reload without restarting Home Assistant.

For example, you can describe an automation that turns on the living room lights at sunset in a YAML file. Moving it half an hour earlier means updating the trigger and applying the file again.

[Create your first automation](tutorials/first-automation.md) to try this workflow with `kubectl`.

## Optional GitOps: changes to your home like changes to your code

Keep your declarations in Git to gain change history and pull-request reviews. A GitOps tool such as Flux can sync merged changes to Kubernetes, where the operator applies them to Home Assistant.

GitOps is optional: the operator manages the same resources whether you apply them yourself or sync them from Git. See the [Flux guide](ecosystem/flux.md) when you want to add automatic synchronization.

## Is this for you?

This project is a good fit if you already run Kubernetes, are comfortable with YAML, and want to manage selected parts of Home Assistant through Kubernetes resources.

**Start with a new installation or a separate instance to try it out.** Migrating an existing Home Assistant installation is possible, but the process is not yet documented or validated end to end. A dedicated migration tool and accompanying documentation are planned.

Using Kubernetes resources and the Home Assistant UI side by side is a supported way to work, not a migration stage. Give each element one owner: for anything you hand over to the operator, update its resource declaration, because the operator will restore that state if you edit it in the UI. Everything else can stay UI-managed. See [resource ownership](explanation/resource-ownership.md) for details.

If you do not already run Kubernetes and mainly want to get Home Assistant running, start with the official [Home Assistant installation options](https://www.home-assistant.io/installation/).

## Finding your way around

This documentation is split by what you came here to do:

| If you want to… | Go to |
|-----------------|-------|
| Learn how this works by doing it | **[Tutorials](tutorials/index.md)** — start with [your first instance](tutorials/first-instance.md) |
| Get one specific job done | **[How-to guides](how-to/index.md)** |
| Look up a field, condition or value | **[Reference](reference/index.md)** |
| Understand why it behaves this way | **[Explanation](explanation/index.md)** |
| Wire the operator into Flux, Vault or another tool | **[Ecosystem guides](ecosystem/index.md)** |

[Start the tutorial →](tutorials/first-instance.md){ .md-button .md-button--primary }

## What can you manage?

Choose the parts of your home you want to manage through Kubernetes:

| What you want to manage | Guide |
|-------------------------|-------|
| Home Assistant instances | [Deploy an instance](how-to/deploy-instance.md) |
| Configuration and secrets | [Manage configuration](how-to/manage-configuration.md) · [Manage secrets](how-to/manage-secrets.md) |
| Automations, scenes, and scripts | [Automations](how-to/manage-automations.md) · [Scenes](how-to/manage-scenes.md) · [Scripts](how-to/manage-scripts.md) |
| Integrations | [Register integrations](how-to/register-integrations.md) |
| Floors, areas, and labels | [Model your house structure](how-to/model-house-structure.md) |
| Community extensions | [Install community repositories](how-to/install-community-repositories.md) |

For resource types and fields, see the [API reference](reference/api.md).

## Help and feedback

Check the [FAQ](reference/faq.md) or [troubleshooting guide](how-to/troubleshoot.md) when you need help. Report bugs and suggest improvements in [GitHub Issues](https://github.com/przemekhys/homeassistant-operator/issues). To contribute, start with the [contributing guide](development/contributing.md).

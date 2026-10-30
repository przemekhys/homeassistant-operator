# Home Assistant Operator

[![Lint](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/lint.yml)
[![Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test.yml)
[![E2E Tests](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/test-e2e-parallel.yml)
[![Security Scan](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml/badge.svg)](https://github.com/przemekhys/homeassistant-operator/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/przemekhys/homeassistant-operator/badge)](https://scorecard.dev/viewer/?uri=github.com/przemekhys/homeassistant-operator)

**Manage Home Assistant like the rest of your Kubernetes homelab.**

Home Assistant Operator lets you manage [Home Assistant](https://www.home-assistant.io/) through the Kubernetes API and tools you already use. Declare the configuration, integrations, and automations you want the operator to manage, and keep using the Home Assistant UI for everything else. Apply resources directly with `kubectl`, or connect your GitOps workflow.

**[Create your first instance →](https://przemekhys.github.io/homeassistant-operator/tutorials/first-instance/)** · [Explore the documentation](https://przemekhys.github.io/homeassistant-operator/)

## Your smart home, familiar tools

- **Use one way to manage your homelab.** Manage selected parts of Home Assistant as Kubernetes resources, alongside the rest of your infrastructure.
- **Manage more than a running container.** Declare automations, scenes, scripts, integrations, and the floors, areas, and labels that describe your home.
- **Skip the manual setup of a new instance.** The operator can complete onboarding, create the initial admin account, and prepare API access for the resources it manages.
- **Choose what to manage declaratively.** Keep a stable base in Kubernetes resources and use the Home Assistant UI for day-to-day control, experiments, and anything you prefer to manage there.

## More than a Home Assistant deployment

If you already run Home Assistant with Helm or your own manifests, you know how to keep it running on Kubernetes. This operator extends that approach to what happens **inside Home Assistant**.

Describe the configuration and behavior you want as Kubernetes resources. The operator keeps the parts it manages aligned with those declarations and reloads supported configuration changes without restarting Home Assistant.

## A small example: lights at sunset

Save this automation as `lights-at-sunset.yaml`:

```yaml
apiVersion: ha.homeassistant.io/v1
kind: HomeAssistantAutomation
metadata:
  name: lights-at-sunset
spec:
  homeAssistantRef:
    name: home
  alias: "Turn on living room lights at sunset"
  triggers:
    - trigger: sun
      event: sunset
  actions:
    - action: light.turn_on
      target:
        entity_id: light.living_room
```

This example assumes an operator-managed instance named `home` in your current namespace and an existing `light.living_room` entity. Apply it with:

```sh
kubectl apply -f lights-at-sunset.yaml
```

The operator creates and reloads the automation in Home Assistant. Want the lights on half an hour earlier? Add `offset: "-00:30:00"` to the trigger and apply the file again.

If you use GitOps, put the same resource in the repository synced to your cluster instead.

## Optional GitOps: changes to your home like changes to your code

Keep your declarations in Git to gain change history and pull-request reviews. With a GitOps tool such as Flux, merged changes can flow automatically to Home Assistant:

```text
Commit and review a change in Git
              ↓
Your GitOps tool syncs it to Kubernetes
              ↓
The operator applies it to Home Assistant
```

GitOps is optional: the operator manages the same resources whether you apply them yourself or sync them from Git.

## Is this for you?

This project is a good fit if you already run Kubernetes, are comfortable with YAML, and want to manage selected parts of Home Assistant through Kubernetes resources.

**Start with a new installation or a separate instance to try it out.** Migrating an existing Home Assistant installation is possible, but the process is not yet documented or validated end to end. A dedicated migration tool and accompanying documentation are planned.

Using Kubernetes resources and the Home Assistant UI side by side is a supported way to work, not a migration stage. Give each element one owner: for anything you hand over to the operator, update its resource declaration, because the operator will restore that state if you edit it in the UI. Everything else can stay UI-managed.

## Get started

The [first-instance tutorial](https://przemekhys.github.io/homeassistant-operator/tutorials/first-instance/) walks you through installing the operator and creating a new Home Assistant instance.

Then choose your next step:

- [Create your first automation](https://przemekhys.github.io/homeassistant-operator/tutorials/first-automation/)
- [Manage Home Assistant configuration](https://przemekhys.github.io/homeassistant-operator/how-to/manage-configuration/)
- [Connect your GitOps workflow with Flux](https://przemekhys.github.io/homeassistant-operator/ecosystem/flux/)
- [Browse all guides and references](https://przemekhys.github.io/homeassistant-operator/)

## Help and feedback

Have a question? Check the [FAQ](https://przemekhys.github.io/homeassistant-operator/reference/faq/) or [troubleshooting guide](https://przemekhys.github.io/homeassistant-operator/how-to/troubleshoot/). Report bugs and suggest improvements in [GitHub Issues](https://github.com/przemekhys/homeassistant-operator/issues).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) or the [contributing guide](https://przemekhys.github.io/homeassistant-operator/development/contributing/).

## Acknowledgments

- [Home Assistant](https://www.home-assistant.io/) - The amazing home automation platform
- [Operator SDK](https://sdk.operatorframework.io/) - Framework for building Kubernetes operators
- [Kubebuilder](https://book.kubebuilder.io/) - SDK for building Kubernetes APIs
- [cert-controller](https://github.com/open-policy-agent/cert-controller) (OPA) - Self-managed rotation of the webhook serving certificate, so the operator needs no cert-manager

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.

# FAQ

*Reference — short answers to recurring questions. Look things up here; it does not teach.*

## Is this the easiest way to run Home Assistant?

No, and it does not try to be. Running Home Assistant on Kubernetes through an
operator is a deliberate engineering choice that pays off if you already run a
cluster and want your smart home in Git. If you do not, [Home Assistant
OS](https://www.home-assistant.io/installation/) will make you happier.

## Do I have to create a HomeAssistantConfiguration?

Yes. A `HomeAssistant` without a `HomeAssistantConfiguration` naming it stays in
`WaitingForConfiguration` and retries. It is not an error and the apply order
does not matter — the instance starts as soon as both exist.

## Can I edit the generated ConfigMaps?

You can, but the next reconcile will overwrite them. Everything the operator
generates is derived from a custom resource, and the custom resource is the only
input. See
[who owns the generated resources](../explanation/resource-ownership.md).

## Will a configuration change restart Home Assistant?

Only if the change touches something Home Assistant cannot reload at runtime.
Automations, scripts, scenes, groups, logger, timers, counters and the `input_*`
helpers reload in place; `homeassistant:` and `mqtt:` need a restart, and so do
`template:` and `zone:`, which the operator does not reload in place. If one change touches both
kinds, the operator restarts. See
[hot reload versus restart](../explanation/reload-vs-restart.md).

## Can I still use the Home Assistant UI?

Yes, for everything the operator does not manage. But anything you *do* manage as
a resource — automations, scenes, scripts, integrations, the configuration file —
will be reset to match the resource. Pick one owner per thing.

## Does the operator need `pods/exec`?

No. That is why the IP-ban recovery works by restarting the pod with an
init-container rather than executing inside it. See
[the security model](../explanation/security-model.md).

## Do I need cert-manager?

Only for TLS on Ingress or Gateway exposure. Without it the operator reports
`CertManagerAvailable=False`, keeps serving over HTTP, and starts issuing
certificates by itself once cert-manager appears. The admission webhook does
**not** need it either — the operator self-signs and rotates its own serving
certificate by default.

## Why does my resource sit in a waiting state instead of failing?

Because a missing prerequisite is not a failure. The operator is level-triggered:
it retries until the world matches the spec, and reserves errors for real faults.
See [the reconciliation model](../explanation/reconciliation-model.md).

## How do I get the API token the operator created?

```sh
kubectl get secret <ha-name>-homeassistant-api-token \
  -o jsonpath='{.data.token}' | base64 -d
```

## Can I run more than one Home Assistant instance?

Yes. Every resource points at one instance through `spec.homeAssistantRef.name`,
so instances are independent. One operator can manage all of them.

## A finalizer is blocking deletion. Now what?

The finalizers are best-effort and complete even when Home Assistant is
unreachable, so this should not happen. If it does, see
[troubleshoot a problem](../how-to/troubleshoot.md#finalizer-blocks-resource-deletion).

## Which Home Assistant versions are supported?

Any version — `spec.version` is passed straight to the image tag. The one
behaviour that depends on the version is how the `http:` section is delivered;
the operator detects that at runtime. See [compatibility](compatibility.md).

# Install HACS-compatible extensions

*How-to — install a community integration, theme, plugin, script or template into an instance. Assumes a running instance.*

!!! warning "Experimental resource"
    `HomeAssistantCommunityRepository` is served from the `v1alpha1` API group. It
    is alpha-quality and carries **no API stability guarantee between releases** —
    fields may change or disappear without a deprecation period. Do not build
    anything you cannot re-do by hand on top of it yet — see
    [what `spec.alpha` means](../explanation/alpha-lifecycle.md).

    If you turn this on, please
    [say how it went](https://github.com/przemekhys/homeassistant-operator/discussions/new/choose)
    — whether it worked is the evidence that decides whether it stays.

This installs extensions that follow the [HACS](https://hacs.xyz/) repository
layout, without HACS itself being installed and without anyone clicking through
its UI. The operator fetches the repository at a ref you pin, checks that its
structure matches the category you declared, and materialises the files into the
instance's configuration volume.

## Prerequisites

- A running Home Assistant instance with [bootstrap completed](bootstrap-instance.md) — the operator needs its API token
- The GitHub repository in `owner/repo` form, and the exact tag, branch or commit you want

## Install a theme

```yaml
apiVersion: ha.homeassistant.io/v1alpha1
kind: HomeAssistantCommunityRepository
metadata:
  name: my-custom-theme
  namespace: default
spec:
  homeAssistantRef:
    name: home
  category: theme
  repository: someuser/some-hacs-theme
  ref: v2.1.0
```

```sh
kubectl apply -f theme.yaml
```

`ref` is required and is never resolved for you. The operator does not track a
"latest" release: an extension only changes version when you change this field,
which is what makes a rollback a `git revert` rather than an investigation.

## Categories

| `category` | What it installs | How it becomes active |
|------------|------------------|-----------------------|
| `integration` | A custom component under `custom_components/` | Requires a restart of the Home Assistant pod |
| `plugin` | A Lovelace frontend resource | Registered as a Lovelace resource, no restart |
| `theme` | A theme file | Theme reload, no restart |
| `python_script` | A Python script | Python-script reload, no restart |
| `template` | A custom Jinja template | Template reload, no restart |

`appdaemon` and `netdaemon` repositories are rejected: they need a separate
runtime that this operator does not deploy.

Only `integration` costs you a restart. The operator triggers it by changing a
hash annotation on the StatefulSet, the same mechanism a configuration change
uses — see [hot reload versus restart](../explanation/reload-vs-restart.md).

## Update to a newer version

Change `ref` and apply again:

```sh
kubectl patch hacr my-custom-theme --type=merge -p '{"spec":{"ref":"v2.2.0"}}'
```

`status.installedVersion` keeps reporting the old ref until the new one has been
fetched, validated **and** activated. A failed update therefore leaves you with a
working installation and an honest status, rather than a broken one that claims
to be fine.

## What cannot be changed

`spec.homeAssistantRef` and `spec.category` are immutable after creation; the API
server rejects the change rather than the operator failing later. To move an
extension to another instance, or to reinstall it under a different category,
delete the resource and create a new one.

## Verify

```sh
kubectl get hacr my-custom-theme
```
```
NAME              HOMEASSISTANT   CATEGORY   PHASE       VERSION   READY   AGE
my-custom-theme   home            theme      Installed   v2.1.0    True    2m
```

`PHASE` walks `Pending → Validating → Installing → Installed`. If it stops at
`Failed`, the reason is in the status:

```sh
kubectl get hacr my-custom-theme -o jsonpath='{.status.lastError}'
```

The [status conditions reference](../reference/conditions.md#homeassistantcommunityrepository)
lists what each reason means; the common ones are `RepositoryUnreachable` (the
ref does not exist), `CategoryMismatch` (the repository declares a different
category than you did) and `TargetConflict` (another resource already installs
the same thing into the same instance).

## Remove an extension

```sh
kubectl delete hacr my-custom-theme
```

The operator removes the installed files before letting Kubernetes delete the
resource. Removal is best-effort: if Home Assistant is unreachable the resource
still deletes, so a broken instance cannot leave you with a stuck object.

## Every field

This guide shows the fields you need for the task. For the complete list of
`HomeAssistantCommunityRepository` fields, with types and defaults, see the
[API reference](../reference/api.md#homeassistantcommunityrepositoryspec).

# What `spec.alpha` means

*Explanation — why some fields live under `spec.alpha`, and what you are signing up for by using one. Nothing here needs a cluster.*

Some fields on a `HomeAssistant` resource sit under `spec.alpha` rather than at
the top level:

```yaml
spec:
  alpha:
    networkPolicy:
      enabled: true
    devices:
      - hostPath: /dev/ttyACM0
```

The prefix is a promise in one direction only: **these fields may change shape or
disappear entirely, in a minor release, with no deprecation period.** Everything
outside `spec.alpha` gets the usual treatment — a deprecation notice, a
migration path, and removal no earlier than the next major version.

## Why have the prefix at all

The alternative would be to keep risky features on a branch until they are
certain, and ship them straight into the stable spec. That sounds safer and is
worse in practice: a feature that touches the pod's security context or its
network path cannot be proven by tests alone. It needs real hardware, real
Zigbee dongles, real CNIs — things a maintainer's CI does not have.

`spec.alpha` buys that feedback without holding the stable API hostage to it.
You get to try the feature; the project gets to change its mind. Both halves of
that bargain are the point, which is why the prefix appears in your YAML rather
than in a changelog footnote: you cannot enable one of these by accident.

## What qualifies

A field starts in `spec.alpha` when enabling it changes something structural
about the running pod — a new container, a change to its security context, a
change to what it can reach on the network. Those are the changes that can break
an instance in ways unit tests do not catch.

Ordinary optional fields with a safe default do not go here. Neither do purely
internal changes you cannot see from the spec. The bar is "could this break
someone's running instance in a way we cannot foresee", not "is this new".

## How a field graduates

```text
spec.alpha.X: false      opt-in, off by default
        ↓
spec.X: false            promoted to the stable spec, still off by default
        ↓
spec.X: true             on by default
        ↓
no longer optional       the behaviour is simply how the operator works
```

Each step is a separate release, and each one is a chance to stop. A field can
also leave the sequence in the other direction: removed outright, because that
is exactly what `alpha` reserves the right to do.

## What is in there today

| Field | What it does |
|-------|--------------|
| `spec.alpha.networkPolicy.enabled` | Creates a NetworkPolicy restricting ingress to the Home Assistant pod |
| `spec.alpha.devices` | Mounts host device nodes, such as a Zigbee or Z-Wave USB coordinator, into the container |

Both change the pod structurally, which is why they are here. `devices` in
particular mounts host paths and alters the container's security context — but
deliberately never grants `privileged: true`, because that would be a far larger
concession than the feature needs.

The current list of fields, with their types, is in the
[API reference](../reference/api.md#alphaspec).

## If you use one

Pin the operator version, read the changelog before upgrading, and be ready to
adjust the field. That is the whole cost.

And then, please, say something. This is the part that actually matters, because
the two things keeping a field in `spec.alpha` are almost never code problems:

- **Does it work on hardware nobody here owns?** A Zigbee coordinator, a
  particular CNI, a storage class that behaves differently under load — none of
  that can be settled by tests. It is settled by someone running it.
- **Does anybody want it?** A field that works perfectly and nobody uses is not a
  candidate for promotion. It is a candidate for removal.

A field cannot graduate on silence. If nothing comes back, the safe assumption is
that it is unproven and unwanted — so the honest thing to do is drop it, and a
feature you rely on disappears for want of one message.

**[Start a discussion](https://github.com/przemekhys/homeassistant-operator/discussions/new/choose)**
— anyone with a GitHub account can, no permissions needed. What helps most:

- which field you enabled, and what you were trying to achieve;
- your hardware and Kubernetes distribution;
- whether it worked, and what you had to change to make it work;
- what you would want it to do that it does not do yet.

"It worked on a Raspberry Pi 5 with k3s, nothing to report" is a genuinely useful
message. It is the kind of evidence that moves a field to the stable spec.

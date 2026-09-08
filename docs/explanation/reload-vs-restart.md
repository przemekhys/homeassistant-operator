# Hot reload versus restart

*Explanation — which configuration changes restart the Home Assistant pod, and why the line is drawn where it is. Nothing here needs a cluster.*

Restarting Home Assistant costs a minute of downtime for every automation, every
integration and every device connection it holds. Reloading a single component
costs nothing noticeable. The operator therefore tries to reload rather than
restart — but only where Home Assistant itself supports reloading that component
at runtime. The list below is not a policy the operator invented; it mirrors what
Home Assistant can genuinely apply without a restart.

## How the decision is made

When `reloadStrategy: auto` is set, the operator parses the YAML diff between the old and new configuration:

- **Hot-reload** (no restart): `automation`, `script`, `scene`, `group`, `logger`, `timer`, `counter`, `input_boolean`, `input_number`, `input_text`, `input_select`, `input_datetime`
- **Restart** (rolling restart): `homeassistant`, `mqtt`, and any unknown top-level key. Also `template` and `zone`: Home Assistant reloads those through dedicated services the operator does not call, so it takes the safe path rather than reporting a reload that did not happen. `http` too — **but only on the YAML delivery path**. On Home Assistant 2026.8+ the `http:` section is delivered through the API (see [HTTP configuration](../how-to/manage-configuration.md#http-configuration-on-home-assistant-20268)) and is excluded from this diff entirely; Home Assistant restarts its own process if the change needs it, without a pod rollout from the operator.

If a single change touches both categories, the operator restarts (safer path).

## Why not always restart

A restart is the simple answer, and it always works. It also costs a minute
during which no automation fires, no sensor is recorded and no integration is
connected — for a change as small as adding one logger line. On a Raspberry Pi
that minute is closer to two.

## Why not always reload

Equally simple, equally wrong. Home Assistant cannot apply a change to
`homeassistant:` or to most integrations without restarting, and reloading
anyway would leave the process running with a configuration that no longer
matches the file. A silently stale instance is worse than a minute of downtime,
because you find out about it much later.

So the operator parses the diff and picks per change. When one change touches
both categories it restarts, because the cost of being wrong in that direction is
a minute, and in the other direction it is a configuration nobody can trust.

## See also

- [Manage configuration](../how-to/manage-configuration.md) — the task itself,
  including `reloadStrategy` if you want to force one behaviour.
- [The reconciliation model](reconciliation-model.md) — why a change that cannot
  be applied yet results in a retry rather than an error.

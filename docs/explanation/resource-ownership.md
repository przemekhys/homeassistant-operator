# Who owns the generated resources

*Explanation — why the operator overwrites hand-edited ConfigMaps, and what that means for a GitOps workflow. Nothing here needs a cluster.*

Every ConfigMap, Secret and workload the operator creates is derived state: on
each pass it is recomputed from its declared inputs — the custom resource, plus
any Kubernetes Secrets that resource points at. The derived object itself is
never an input. Editing it directly is therefore not "a change the operator will
merge"; it is a difference the next reconcile will erase, usually within seconds.

This is what makes the whole setup safe to drive from Git. Because derived state
is never authoritative, a manual edit made in a hurry during an incident is
undone automatically rather than silently becoming the new truth nobody
remembers making.

Note what that does and does not buy you: the operator keeps derived objects
matching their inputs, but keeping those *inputs* matching your repository is a
separate job, done by a GitOps controller reconciling the custom resources and
Secrets themselves — see [Flux CD](../ecosystem/flux.md).

## Configuration ConfigMap

The generated ConfigMap is owned exclusively by the operator. Any direct edits to the ConfigMap are detected and reverted on the next reconcile. **Always edit the `HomeAssistantConfiguration` CR**, not the ConfigMap.

Editing `home-configuration` directly is not "a change the operator will pick
up" — the next pass regenerates that ConfigMap from the
`HomeAssistantConfiguration` and your edit is gone. Change the resource instead:
[manage configuration](../how-to/manage-configuration.md).

## How secrets are composed

1. The operator reads all referenced Kubernetes Secrets
2. It merges their keys into a single YAML document
3. The result is stored in a Kubernetes `Secret` named `<ha-name>-generated-secrets`
4. A SHA-256 hash of the content is written to the StatefulSet pod template annotation
5. Kubernetes detects the annotation change and performs a rolling restart (configurable)

## The generated secrets Secret

The composed `secrets.yaml` lands in a Kubernetes `Secret` named
`<ha-name>-generated-secrets`. It is derived state like any other: edit the
source Secrets or the `HomeAssistantSecrets` resource, never the generated one.
See [manage secrets](../how-to/manage-secrets.md).

## What this costs you

The honest trade-off: you cannot make a quick fix in the cluster and have it
stick. During an incident that feels like an obstacle. It is the same property
that guarantees the cluster still matches your repository afterwards, when
nobody remembers what was changed at 3am — and that is worth more than the
convenience it takes away.

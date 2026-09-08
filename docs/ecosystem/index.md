# Ecosystem guides

*Ecosystem guides — how tools this project does not ship work together with the operator. Not part of the operator's supported API.*

These pages show how the operator fits alongside other things you probably
already run: a GitOps engine, a secret manager. They exist because the questions
keep coming up, not because the operator has any special support for these tools.

!!! note "Not part of the supported API"
    Unlike the rest of this documentation, these guides are **not** part of the
    operator's supported API. Each one names the tool versions it was checked
    against; a different version may need adjusting. They will not be updated in
    step with those tools' releases.

## Available guides

| Guide | What it solves |
|-------|----------------|
| **[Flux CD](flux.md)** | Deploying the operator and its resources from Git, and auto-updating the Home Assistant image with a policy you control |
| **[External secret management](secrets-management.md)** | Sourcing Home Assistant's secrets from External Secrets Operator, Sealed Secrets or Vault instead of `kubectl create secret` |

## What belongs here

A tool qualifies if the project neither ships nor controls it, and integrating it
meaningfully changes how you work with the operator.

A tool does **not** qualify if:

- it is a feature of the operator itself — that belongs in the
  [how-to guides](../how-to/index.md);
- the integration amounts to "it works, because these are ordinary Kubernetes
  resources" — there is nothing to write;
- nobody has actually tried it. A page without verified versions breaks the one
  promise these guides make.

Some third-party tools appear inside a how-to guide rather than here, because
they are one way of finishing a task rather than a topic of their own — cluster
backups in [configure backups](../how-to/configure-backups.md), and admission
policy in [verify signed releases](../how-to/verify-signed-releases.md). Those
sections carry the same support boundary as a full page: where the content sits
does not change what it promises.

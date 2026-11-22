# Why migration has safety gates

*Explanation — the reasoning behind the deliberately constrained migration workflow.*

Migrating an existing Home Assistant is not the same as applying a desired Kubernetes resource. It combines a backup created by an external system, a restore that changes a new system, and a traffic decision that can affect a physical home. The operator treats those as separate decisions rather than one reconciliation loop.

## Evidence is more useful than optimism

A timeout after a mutating Home Assistant request does not say whether the request ran. Retrying could create a second backup or restore a second time. For that reason an uncertain result becomes `Unknown`. The next safe action is to observe evidence for the same request or to abort it; it is not to retry.

The operation records identifiers, digests, Secret references, and receipts so that an approval applies to exactly the inputs an administrator reviewed. It does not place credentials or backup contents in the custom resource, where they could leak through Git, status, or logs.

## The source and target must not compete

Two active instances can both operate integrations, write to devices, or send notifications. A restore therefore requires evidence that the source is stopped and a separate restore approval. The operator observes an in-cluster source but does not scale it down; external sources remain an administrator responsibility.

The target starts in a guarded restore mode. Its normal bootstrap and configuration projections stay disabled until restore verification and the subsequent approvals have completed. This avoids mixing a restored `/config` with new declarative configuration during the sensitive part of the workflow.

## Cutover remains a human decision

The operator does not know which ingress path, DNS record, or physical-device consequence makes a target safe to expose. It therefore never changes Service, Ingress, or Gateway routing. A verified restore leads to a `resume` approval, then a distinct `manual-cutover` approval. The separation preserves an observation window between technical validation and user-visible traffic.

The workflow is experimental because the safety model has been validated only in its documented test environment. The [migration how-to](../how-to/migrate-existing-instance.md) describes the operational sequence; the [migration reference](../reference/migration.md) defines the resource contract.

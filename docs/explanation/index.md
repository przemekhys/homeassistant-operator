# Explanation

*Explanation — the ideas behind the operator's behaviour. Nothing here needs a cluster.*

These pages are for reading, not for following. They explain the decisions that
shape how the operator behaves, so that its surprises stop being surprises. None
of them is required to complete any [how-to guide](../how-to/index.md).

- **[The reconciliation model](reconciliation-model.md)** — why a resource waits
  instead of failing, and how each resource reaches Home Assistant.
- **[Hot reload versus restart](reload-vs-restart.md)** — which configuration
  changes restart the pod, and why the line is drawn where it is.
- **[Who owns the generated resources](resource-ownership.md)** — why the
  operator overwrites hand-edited ConfigMaps, and what that means for GitOps.
- **[What `spec.alpha` means](alpha-lifecycle.md)** — why some fields carry an
  experimental prefix, and what you are signing up for by using one.
- **[The security model](security-model.md)** — what the operator is allowed to
  do, and where the boundary between operator and workload runs.

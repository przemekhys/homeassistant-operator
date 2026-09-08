# Reference

*Reference — complete, uniform descriptions of what exists. Look things up here; it does not teach.*

| Page | What it catalogues | Source of truth |
|------|--------------------|-----------------|
| [API reference](api.md) | Every field of every custom resource | Generated from the Go types on each publish |
| [Status conditions](conditions.md) | Every condition and `reason` the operator sets | The controller source |
| [Events](events.md) | Every Kubernetes Event the operator emits | The controller source |
| [Helm chart values](helm-values.md) | Every chart installation value | Generated from the chart's `values.yaml` |
| [Compatibility](compatibility.md) | Supported Kubernetes, Home Assistant and Go versions | Chart metadata and CI configuration |
| [FAQ](faq.md) | Recurring questions with short answers | Hand-maintained |
| [Changelog](changelog.md) | Released versions and what changed | The repository's changelog |

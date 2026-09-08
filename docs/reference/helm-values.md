# Helm chart values

*Reference — every value you can set when installing the operator. Look things up here; it does not teach.*

Generated from the chart's `values.yaml`, so this table cannot drift from what
the chart actually accepts — a CI gate fails the build if it does.

To see the same list from your own machine, including for a specific released
version:

```sh
VERSION=1.4.0
helm show values oci://ghcr.io/przemekhys/charts/homeassistant-operator --version "$VERSION"
```

For how to apply these, see [install the operator](../how-to/install-operator.md).

## Values

{%
  include-markdown "../../charts/homeassistant-operator/README.md"
  start="## Values"
%}

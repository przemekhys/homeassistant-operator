# Upgrade from 1.6 to 1.7

*How-to — complete the required migration before following the general [operator upgrade procedure](upgrade-operator.md).*

## Migrate HomeAssistantCommunityRepository

Version 1.7 serves `HomeAssistantCommunityRepository` only at
`ha.homeassistant.io/v1`. Before applying the 1.7 CRD, change every community
repository from `v1alpha1` to `v1`; the operator does not do this automatically.

1. Update source-controlled manifests to use `apiVersion:
   ha.homeassistant.io/v1`. Do not create a second resource for the same
   extension.
2. Confirm that Kubernetes has also saved the existing objects as `v1`, not
   `v1alpha1`:

   ```sh
   kubectl get crd homeassistantcommunityrepositories.ha.homeassistant.io \
     -o jsonpath='{.status.storedVersions}{"\n"}'
   ```

If the output still contains `v1alpha1`, use the storage-version migration
procedure supported by your cluster to save those existing objects as `v1`.
Continue with the general upgrade only when the output contains `v1` and does
not contain `v1alpha1`.

The removed alpha endpoint rejects old manifests. A rollback to a release that
requires alpha is not supported after this storage migration.

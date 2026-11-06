# Recover a native backup

*How-to — validate a Home Assistant Container backup on a fresh isolated target.*

The operator does not restore a running instance. Native restore replaces data
on its target and restarts Home Assistant, so use a new Container with a fresh,
writable `/config` directory. Do not use an operator-managed instance: its
projected configuration files can hide restored files.

Before recovery, obtain the stored archive from the selected Home Assistant
backup agent. If it is encrypted, also obtain its matching encryption key from
the retained Kubernetes Secret. Keep both outside the cluster in protected
storage. Download the original archive; passing a password to Home Assistant's
download endpoint decrypts it and creates a different, highly sensitive export.

Start a new Container using an image version at least as new as the archive,
without device mounts and isolated from the source instance and its devices.
Use the native onboarding backup upload and restore flow, choose the archive,
and enter the matching key if it is encrypted. Confirm the destructive action
only after checking that the target is the fresh Container. The documented
recovery validation covers protected archives, not unencrypted ones.

After restart, verify representative entity or device identity, an integration
configuration, and data not declared by the operator. External databases,
apps, hardware access, and cloud credentials are outside this procedure and
may need separate recovery or re-authentication. Do not treat a cluster-local
Secret as the only recovery copy.

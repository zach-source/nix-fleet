# Runtime secret migration (F01 / F04)

These options are opt-in. No host settings, listener defaults, existing Dolt
accounts, passwords, or grants are changed by this migration automatically.
Legacy inline passwords and webhook secrets remain
supported and remain readable in Nix store/public script artifacts until the
owner migrates. Restrictive destination modes do not protect the store copy.

Provision runtime files through the existing encrypted-secret mechanism or
another trusted process. Use string paths (e.g. `"/run/private/key"`), never Nix
path literals, `builtins.readFile`, or managed-file `text`/`source` containing
plaintext secrets. Keep parent directories private and owned by the intended
administrator. Do not point these options into the Nix store.

## Dolt

For an existing authenticated host such as gti, preserve its SQL accounts and
passwords. First verify that the intended accounts already have the necessary
SQL and `CLONE_ADMIN` grants. Then opt out of legacy provisioning:

```nix
nixfleet.modules.dolt = {
  enable = true;
  provisionRemoteRoot = false;
  serverConfigFile = "/run/private/dolt-server.yaml";
  password = "";
};
```

`serverConfigFile` is a complete, externally provisioned Dolt YAML configuration,
readable by the Dolt service user only (e.g. 0600 dolt:dolt). It replaces all
generated YAML settings, including user, listener, behavior, data_dir, cfg_dir,
and remotesapi. Keep data_dir/cfg_dir consistent with the module's `dataDir` and
preserve the current ports/listener/authentication settings during migration.
The module still manages the Unix account, directory, and service. If runtime
files are provisioned at boot, arrange provisioning before dolt-server starts.
No runtime YAML file is created or overwritten by this module.

With legacy provisioning enabled, **new** `root@%` receives the configured
legacy password. `CREATE USER IF NOT EXISTS` never resets an existing password,
including an existing empty password. The default empty password and network
listener remain unchanged for compatibility. Disabling provisioning neither
revokes grants nor deletes accounts. Existing passwordless remote accounts need
an explicit owner-managed migration; this change does not silently secure them.

## Skipped PKI subcase

F04's PKI subcase was not confirmed reachable against the current core module.
Importing `modules/pki.nix` fails because `nixfleet.environment` is undeclared.
After experimentally removing that blocker, the default-disabled rotation hook
still emits the undeclared `nixfleet.hooks.pre-cert-reload` option. Enabled
rotation also uses a unit schema incompatible with the core text-unit option.
No PKI option, profile, or rotation changes are included in this fix.

## Pull-mode webhook

Deploy a nixfleet binary containing `pull-mode webhook-signature` before opting
in. Provision a root-only key file on each destination, then use either:

```nix
nixfleet.pullMode.webhook = {
  url = "https://example.invalid/hook";
  secretFile = "/run/private/webhook-key";
  secret = null;
};
```

Or install pull mode with `--webhook-secret-file /run/private/webhook-key`
instead of `--webhook-secret`. The flag is a **remote** path; the installer does
not read or transfer its contents. Use a persistent file or arrange boot-time
provisioning before the pull timer runs.

The signer reads the key at notification time and accepts payload bytes on
stdin. Only the path and HMAC signature appear in arguments. Missing/empty keys
or a missing signer fail the notification without sending an unsigned fallback.
Key bytes are used verbatim: do not add a trailing newline unless it is part of
the receiver's key. Legacy inline mode remains unchanged.

Prior public artifacts are not removed by switching options. Owners must
separately assess any previously exposed credentials and coordinate cleanup or
rotation. This work performs no host apply, version bump, decryption, or rotation.

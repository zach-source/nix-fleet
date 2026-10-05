# macOS hosts (`base = "darwin"`)

NixFleet deploys to macOS through [nix-darwin](https://github.com/LnL7/nix-darwin).
Support is real but **narrower than Ubuntu's**, and the gaps are refused at
evaluation time rather than silently dropped.

Declare the host in `flake.nix` under `darwinConfigurations`, not
`nixfleetConfigurations`:

```nix
darwinConfigurations = {
  mac-1 = mkDarwinFleetConfiguration {
    modules = [ ./hosts/mac-1.nix ];
  };
};
```

`nixfleet apply` resolves a darwin host as
`darwinConfigurations.<name>.system`, so a host missing from that attrset
cannot be deployed at all.

## What works

| `nixfleet.*` option | Translates to |
|---|---|
| `packages` | `environment.systemPackages` |
| `files` (under `/etc` only) | `environment.etc` |
| `users` | `users.users` |
| `groups` | `users.groups` |
| `directories` | `mkdir`/`chmod`/`chown` in the activation script |
| `hooks.preActivate` | start of `system.activationScripts.postActivation` |
| `hooks.postActivate` | end of `system.activationScripts.postActivation` |
| `healthChecks` | read by the CLI, not the backend — works as everywhere |

Native nix-darwin options (`system.defaults`, `homebrew`, `launchd`,
`programs.*`) can be used alongside `nixfleet.*` in the same host file. The
backend sets no `system.defaults` of its own: those options are
primary-user-requiring, so a default there would force every host to also
set `system.primaryUser` for a Dock preference it never asked for.

## What is not supported

Each of these raises an assertion naming the offending attribute. They are not
TODOs that will be quietly filled in — the first one in particular is a design
decision, not a missing feature.

### `nixfleet.systemd.units`

launchd has no equivalent of a systemd unit file, and NixFleet does not
translate one.

This is worth being explicit about, because the backend used to pretend
otherwise. The old translation read only `enabled` and threw the unit body
away, so every declared service compiled to a plist with no program to run —
an apply that reported success and started nothing.

A better parser would not fix it. `Type=`, `After=`/`Wants=`/`Requires=`,
`Condition*=`, `Restart=` and ordered `ExecStartPre=` chains have no launchd
counterparts, and this fleet's units lean on all of them. Declare
`launchd.daemons` natively instead.

### `nixfleet.secrets`

The Ubuntu backend decrypts age secrets during activation using the host's SSH
key. The darwin backend has no equivalent step. Secrets have to be placed by
other means.

### `nixfleet.files` outside `/etc`

`environment.etc` is the only file mechanism nix-darwin offers, and it can only
write under `/etc`.

### `mode`, `owner` and `group` on `nixfleet.files`

`environment.etc` deploys **store symlinks**, so the file on disk carries the
store's ownership and mode (root, `0644`) whatever the config says. Rather than
deploy a file whose permissions differ from the declaration, the backend
refuses. Use `hooks.postActivate` if a specific mode is required.

### `nixfleet.apt`

macOS has no apt.

## Tests

`tests/default.nix` has three darwin checks: `darwin-base-is-declarable`,
`darwin-host-evaluates` and `darwin-refuses-what-it-cannot-do`.

The last two evaluate a real nix-darwin system, so they only exist on a darwin
builder — `nix flake check` runs them on a Mac, and CI (linux-only) skips
them. `darwin-base-is-declarable` needs no nix-darwin and runs everywhere.

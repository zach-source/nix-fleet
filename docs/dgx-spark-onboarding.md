# Onboarding a DGX Spark to NixFleet

Getting a Spark from "unboxed" to "managed by `nixfleet apply`".

A Spark is managed exactly like an Ubuntu host — same apt, systemd and
filesystem layout — with two differences that shape this whole procedure:

- **It is aarch64.** Your x86_64 fleet cannot build for it, and neither can a
  Mac without help. See [step 3](#3-teach-your-workstation-to-build-aarch64-linux).
- **NixFleet never OS-updates it.** `base = "dgx"` structurally excludes it from
  every `os-update` path. That lane belongs to DGX Dashboard and `fwupdmgr`.

Refs: [OS and component update](https://docs.nvidia.com/dgx/dgx-spark/os-and-component-update.html) ·
[Spark clustering](https://docs.nvidia.com/dgx/dgx-spark/spark-clustering.html)

## 0. What is already wired

`hosts/dgx-spark-{1,2}.nix`, `modules/dgx-spark-cluster.nix` and
`modules/vllm.nix` exist, and both Sparks are registered in `flake.nix`
(pinned to `aarch64-linux`) and `inventory/fleet.yaml`.

Confirm before you start:

```bash
nix eval --raw .#nixfleetConfigurations.dgx-spark-1.base   # -> dgx
```

Both checks are local. `nixfleet status -g dgx` will resolve the hosts but
cannot connect to anything until steps 1 and 2 are done.

## 1. Addresses

Set, and verified against the hardware on 2026-08-20:

| Host | Management (`enP7s7`, 10GbE) | Storage VLAN 8 | CX7 fabric |
|------|------------------------------|----------------|-----------|
| dgx-spark-1 (`spark-5267`) | 192.168.3.140 | 192.168.8.140/24 | .100.10 / .101.10 |
| dgx-spark-2 | 192.168.3.141 | 192.168.8.141/24 | .100.11 / .101.11 |

Only the management address goes in `inventory/fleet.yaml` and
`nixfleet.host.addr`. The other two are produced by
`modules/storage-vlan.nix` and `modules/dgx-spark-cluster.nix`.

## 2. Bootstrap each Spark

One script does all three password-requiring steps — `nixbot`, Nix + `deploy`,
and the trusted-users seed. It is idempotent, so re-running is safe:

```bash
scp scripts/{bootstrap-ubuntu.sh,dgx-spark-setup.sh} <you>@<spark>:/tmp/
ssh -t <you>@<spark> 'sudo bash /tmp/dgx-spark-setup.sh'
```

`-t` because `ztaylor` has **no passwordless sudo** on a fresh Spark (verified on
spark-5267) — you will be prompted. That is precisely why this step cannot be
driven from the workstation, and why it is the only manual one.

What it does, and why each part is needed:

| Step | Why it isn't already handled |
|------|------------------------------|
| `nixbot` (uid 30033, `NOPASSWD: ALL`) | Exists **only on gti**, not on any gtr node, so there is no host to copy from. Grants full passwordless root to either fleet key — gti's existing posture, stated rather than inherited. |
| `bootstrap-ubuntu.sh` | Delegated to, not duplicated. Installs Nix, creates `deploy`, writes sudoers, seeds `/var/lib/nixfleet`. |
| trusted-users seed | A genuine chicken-and-egg: `modules/nix-config.nix` owns `trusted-users`, but it can only arrive via an apply that the missing trust blocks. Also adds the `!include nix.custom.conf` line if the installer didn't, since otherwise the seed is read by nobody. |

It refuses to run on anything without `/etc/dgx-release`. DGX OS reports
`ID=ubuntu` and `PRETTY_NAME="Ubuntu 24.04.4 LTS"`, so os-release alone cannot
identify a Spark — use `bootstrap-ubuntu.sh` directly for plain Ubuntu.

Confirm from the workstation:

```bash
ssh -o KexAlgorithms=curve25519-sha256 nixbot@192.168.3.140 'sudo -n id'
```

## 3. Teach your workstation to build aarch64-linux

Nothing in the fleet can build for a Spark. gti and the gtr nodes are x86_64;
a Mac is `aarch64-darwin` — the right CPU, the wrong kernel. **Each Spark builds
for itself.**

The scope is smaller than it sounds. A `--dry-run` of `dgx-spark-1.system`
wants **9 derivations built and 141 paths fetched**: everything substantial
comes from cache.nixos.org, which already carries aarch64-linux. The nine are
NixFleet's own outputs — the activation script, unit files, `nixfleet-etc`, and
the two netplan configs.

```bash
sudo bash scripts/dgx-add-builder.sh
```

Root is required for a non-obvious reason: **the nix daemon performs remote
builds, and it runs as root**. So it is root's `~/.ssh/known_hosts` that has to
trust the Sparks, not yours. A missing entry surfaces as:

```
cannot build on 'ssh-ng://deploy@192.168.3.140':
  error: failed to start SSH master connection to '192.168.3.140'
Failed to find a machine for remote build!
```

which names neither SSH keys nor `known_hosts`. The script handles it, plus:

- appends both Sparks to `/etc/nix/machines` (the key stays in **your** home —
  root reads it fine, and that matches how the existing gti builder is set up)
- sets `builders-use-substitutes = true`, so the Spark pulls its 161 MiB from
  cache.nixos.org directly rather than the workstation downloading it all and
  pushing it over SSH
- restarts the right daemon. Determinate Nix replaces `org.nixos.nix-daemon`
  with `systems.determinate.nix-daemon` and leaves the old label
  present-but-**disabled**, so the script probes rather than assuming

Verify:

```bash
nix build --impure --no-link --print-out-paths \
  .#nixfleetConfigurations.dgx-spark-1.system
```

> Don't reach for qemu-emulated aarch64 on gti. It's Ubuntu, not NixOS, so
> there's no `boot.binfmt.emulatedSystems`, and emulated builds are far slower
> than letting the Spark do its own nine derivations.

## 4. Onboard

Gets the host's age key from its SSH host key and rekeys the secrets:

```bash
nixfleet host onboard -H dgx-spark-1 --skip-pull-mode
```

Add the printed key to `secrets/secrets.nix` under `hosts`, then:

```bash
nixfleet secrets rekey
```

Repeat for `dgx-spark-2`.

## 5. First apply — with vLLM off

`hosts/dgx-spark-1.nix` imports `dgx-spark-dsv4.nix`, which declares a vLLM unit
pointing at `/opt/dsv4/bin/dsv4-vllm-entrypoint`. That entrypoint ships with the
vendor recipe and **is not on a fresh box**, so a first apply would enable and
start a unit that immediately fails.

Turn the service off for the onboarding apply — add to each host file:

```nix
nixfleet.modules.vllm.services.dsv4-flash.enable = false;
```

That drops the unit and its health check entirely, leaving only
`nixfleet-nix-config-apply.{service,path}`. Verify before applying:

```bash
nix eval --json --apply builtins.attrNames \
  .#nixfleetConfigurations.dgx-spark-1.config.nixfleet.systemd.units
```

Then, one host at a time:

```bash
nixfleet plan  -H dgx-spark-1      # read-only, shows the diff
nixfleet apply -H dgx-spark-1
```

## 6. Verify

```bash
nixfleet status -g dgx
nixfleet run -g dgx -- 'nvidia-smi -L'
```

Expect these health checks to pass: `gpu-present`, `dgx-dashboard`,
`nixfleet-nix-config-applied`. The CX7 checks fail until step 7 — that is
correct, not a regression.

Two known-good oddities, neither a fault:

- `nvidia-smi` reports **"Memory-Usage: Not Supported"** on Spark. Documented
  platform behaviour. Do not health-check GPU memory here.
- DGX Dashboard binds **loopback** on `:11000`. Reach it with
  `ssh -L 11000:localhost:11000 <you>@<spark>` rather than opening a hole.

## 7. Bring up VLAN 8 and the ConnectX-7 fabric

Applying the config only *writes* `/etc/netplan/40-cx7.yaml` and
`60-storage-vlan8.yaml`. Nothing takes effect until `netplan apply`, and that is
the one genuinely dangerous step on a box with no BMC and no reachable KVM.

### Do it behind a deadman

Arm a rollback first, then apply. If the change kills the network, the box
repairs itself in four minutes instead of needing a drive.

```bash
# 1. arm
ssh nixbot@<spark> 'sudo systemd-run --on-active=240 --unit=netplan-deadman --collect \
  /bin/bash -c "mkdir -p /root/netplan-bak && \
    mv /etc/netplan/40-cx7.yaml /etc/netplan/60-storage-vlan8.yaml /root/netplan-bak/ 2>/dev/null; \
    netplan apply"'

# 2. apply, detached — netplan apply drops your SSH session, so run it under
#    systemd or the disconnect kills it mid-reconfigure
ssh nixbot@<spark> 'sudo systemd-run --unit=netplan-apply-now --collect --no-block netplan apply'

# 3. verify, then disarm
ssh nixbot@<spark> 'sudo systemctl stop netplan-deadman.timer && \
  sudo systemctl reset-failed netplan-deadman.timer netplan-deadman.service'
```

This is not theoretical. The first attempt on spark-7ee2 took the box off the
network for ~104 seconds until the deadman restored it. See below for why.

### Why the first attempt failed: DGX OS is NetworkManager

`/etc/netplan/00-installer-config.yaml` on a Spark is an empty stub:

```yaml
network:
  version: 2
  renderer: NetworkManager
```

It declares **no interfaces at all**. The management address comes from an
auto-created NetworkManager profile ("Wired connection 3", `ipv4.method: auto`),
entirely outside netplan.

The gtr nodes work differently: `50-cloud-init.yaml` declares the parent's
addressing, and netplan **merges** `ethernets` definitions across files, so a
second file supplying only `mtu:` is additive.

On a Spark there is nothing to merge with. A parent declared with only an MTU
becomes the *entire* definition, NetworkManager swaps its working profile for an
address-less one, and the host vanishes. Hence
`modules.storageVlan.parentDhcp = true`, which emits `dhcp4`/`dhcp6` alongside
the MTU. Set it on any host where netplan does not already own the parent.

Note the MTU itself was never the problem — `enP7s7` reports `maxmtu 9194` and
stayed reachable at 9000 throughout.

### Cabling

Two rules from NVIDIA's playbook, neither enforceable from Nix:

1. Cable the **same physical port** on every node.
2. Use the **same username** on every node — `discover-sparks` assumes `$USER`
   resolves identically across the fabric.

The trap: **each QSFP port surfaces as two Linux interfaces**, because the NIC
reaches the SoC over two PCIe Gen5 x4 links. One cable gives *two* `(Up)` lines.

| Port | Interfaces |
|------|-----------|
| Left (nearest the RJ45, from the rear) | `enp1s0f0np0`, `enP2p1s0f0np0` |
| Right | `enp1s0f1np1`, `enP2p1s0f1np1` |

This pair is cabled on the **left** port, so both host files override the
module's right-port default. Left and right are electrically identical — the
playbook simply illustrates the right one, which is not a reason to move a
cable.

### Verified end state

Both Sparks, 2026-08-20:

| | dgx-spark-1 (spark-5267) | dgx-spark-2 (spark-7ee2) |
|---|---|---|
| Management | 192.168.3.140/24, mtu 9000 | 192.168.3.141/24, mtu 9000 |
| VLAN 8 | 192.168.8.140/24, mtu 9000 | 192.168.8.141/24, mtu 9000 |
| CX7 | 192.168.100.10, 192.168.101.10 | 192.168.100.11, 192.168.101.11 |
| Jumbo to gtr-150 | PASS (8972B, DF) | PASS |
| Health checks | 9/9 PASS | 8/8 PASS |

The jumbo ping matters more than a plain one: an untrunked or
non-jumbo switch port still answers a normal ping while silently dropping
anything over 1500.

## 8. Enable dsv4

Only once `/opt/dsv4/bin/dsv4-vllm-entrypoint` and the pinned vLLM dev wheel
(`0.21.1rc1.dev339+g1967a5627bc3`) exist on both boxes. Neither is packaged by
NixFleet.

Drop the `enable = false` lines from step 5, then apply **rank 1 first** — rank 0
serves `:8000` and expects its peer to be waiting at the rendezvous:

```bash
nixfleet apply -H dgx-spark-2
nixfleet apply -H dgx-spark-1
curl -s localhost:8000/v1/models   # via an ssh -L tunnel to rank 0
```

`NCCL_SOCKET_IFNAME` is pinned to the CX7 interface in
`hosts/dgx-spark-dsv4.nix`. Left unset, NCCL may quietly pick the 10GbE
management NIC — the job still runs, at a fraction of the bandwidth, and it is
miserable to diagnose.

## What NixFleet will refuse to do

Structural, not a convention to remember:

| Path | Behaviour on `base = "dgx"` |
|------|------------------------------|
| `nixfleet os-update *` | host filtered out (`SupportsOSUpdates` is false) |
| background update scheduler | skipped |
| `POST /api/hosts/{name}/apt/upgrade` | **400** |
| apt install / remove / autoremove / clean | works normally |
| read-only apt endpoints | work normally |

To actually update a Spark, use DGX Dashboard, or by hand:

```bash
sudo apt update && sudo apt dist-upgrade
sudo fwupdmgr refresh && sudo fwupdmgr upgrade && sudo reboot
```

No apt holds are declared. NVIDIA's guide names no metapackages and does not ask
you to pin anything, so inventing holds would only fight the dashboard.

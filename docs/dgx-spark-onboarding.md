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

## 1. Set the real addresses

`10.0.3.10` / `10.0.3.11` are placeholders. Replace them in **both** places or
the two halves disagree and you deploy to nothing:

| File | Field |
|------|-------|
| `inventory/fleet.yaml` | `hosts.dgx-spark-{1,2}.addr` |
| `hosts/dgx-spark-{1,2}.nix` | `nixfleet.host.addr` |

The management address is the 10GbE RJ45 port. The `192.168.100.x` /
`192.168.101.x` addresses are the ConnectX-7 fabric and are configured
separately in [step 7](#7-cable-and-bring-up-the-connectx-7-fabric) — never put
one of those in the inventory.

## 2. Bootstrap each Spark

Run on the box, as root, once per Spark. Installs Nix multi-user, creates
`deploy`, writes the sudoers rules and seeds `/var/lib/nixfleet`:

```bash
scp scripts/bootstrap-ubuntu.sh <you>@<spark>:/tmp/
ssh <you>@<spark> 'sudo bash /tmp/bootstrap-ubuntu.sh --ssh-key "$(cat ~/.ssh/nixfleet.pub)"'
```

> The script gates on `grep -q Ubuntu /etc/os-release`. DGX OS is an Ubuntu
> derivative and should pass, but check `cat /etc/os-release` first — if NVIDIA
> has rebranded `NAME=`, the gate is the only thing that needs relaxing.

### Seed trusted-users by hand

This is the one genuine chicken-and-egg in the whole process, and it is not in
the bootstrap script. `nixfleet apply` copies **locally-built, unsigned**
closures, which the daemon rejects unless `deploy` is a trusted user — but the
module that manages `trusted-users` can only arrive *via* an apply.

So seed it manually, once:

```bash
ssh <you>@<spark> 'echo "extra-trusted-users = deploy" | sudo tee -a /etc/nix/nix.custom.conf \
  && sudo systemctl restart nix-daemon'
```

`modules/nix-config.nix` takes over from the first apply onward.

## 3. Teach your workstation to build aarch64-linux

Nothing in the fleet can build for a Spark. gti and the gtr nodes are x86_64;
a Mac is `aarch64-darwin`, which is the wrong kernel even though the CPU
matches. **The Spark builds for itself**, as a remote builder.

`nix build` is invoked as a plain subprocess by the CLI, so global builder
config applies with no NixFleet flags needed.

`/etc/nix/machines` on your workstation:

```
ssh-ng://deploy@<spark-1-addr> aarch64-linux /var/root/.ssh/nixfleet 8 1 big-parallel
```

Then in `/etc/nix/nix.custom.conf`:

```
builders-use-substitutes = true
```

The nix daemon runs as **root**, so root — not you — needs the key and the host
key:

```bash
sudo cp ~/.ssh/nixfleet /var/root/.ssh/nixfleet
sudo chmod 600 /var/root/.ssh/nixfleet
sudo ssh-keyscan -H <spark-1-addr> >> /var/root/.ssh/known_hosts
sudo launchctl kickstart -k system/org.nixos.nix-daemon

# prove it
nix build --impure --no-link --print-out-paths \
  .#nixfleetConfigurations.dgx-spark-1.system
```

If that prints a store path, everything downstream works. If it hangs, it is
almost always root's `known_hosts`.

> Do **not** reach for qemu-emulated aarch64 on gti. It is Ubuntu, not NixOS, so
> there is no `boot.binfmt.emulatedSystems`, and emulated builds of this size
> are measured in hours.

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

## 7. Cable and bring up the ConnectX-7 fabric

Two rules from NVIDIA's playbook, neither enforceable from Nix:

1. Cable the **same physical port** on both machines.
2. Use the **same username** on both — `discover-sparks` assumes `$USER`
   resolves identically across the fabric.

The trap: **each QSFP port surfaces as two Linux interfaces**, because the NIC
reaches the SoC over two PCIe Gen5 x4 links. One cable gives *two* `(Up)` lines.

```bash
ssh <you>@<spark> ibdev2netdev
```

| Port | Interfaces |
|------|-----------|
| Left (nearest the RJ45, from the rear) | `enp1s0f0np0`, `enP2p1s0f0np0` |
| Right | `enp1s0f1np1`, `enP2p1s0f1np1` |

`modules/dgx-spark-cluster.nix` defaults to the **right** port. If you cabled
the left, override `interfaces` in the host file. Only `(Up)` interfaces are
cabled — addressing a down interface silently does nothing.

The module writes `/etc/netplan/40-cx7.yaml` (mode 0600), but netplan needs a
nudge; `dgx-cx7-addresses` exists specifically to catch a file that was written
and never applied:

```bash
nixfleet apply -g dgx
nixfleet run -g dgx -- 'sudo netplan apply'
nixfleet run -g dgx -- 'ping -c1 -W2 192.168.100.10'
```

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

# NixFleet: Managing Ubuntu + NixOS Servers with Nix (Ansible-like)

This document captures a design for an **agentless** fleet-management system that uses **Nix** as the desired‑state engine to manage **Ubuntu servers (Nix-on-Ubuntu)** and **NixOS hosts**, with **first-class OS update + reboot orchestration**.

---

## Goals

- **One repo, one inventory, one CLI** to manage both:
  - **Ubuntu** (keep Ubuntu as the OS; use Nix for deterministic software/config/services)
  - **NixOS** (full declarative system via NixOS modules)
- **Ansible-like UX**:
  - `plan`, `apply`, `rollback`, `status`, `run`
  - inventory groups, parallelism, canaries
- **First-class OS updates**
  - Ubuntu: security/full upgrades, holds/pinning, reboot windows, rolling reboots
  - NixOS: switch to new generation; reboot if needed (kernel/initrd change)

---

## Non-goals

- Turning Ubuntu into NixOS.
- Baking secrets into `/nix/store`.
- Replacing kernel/bootloader management on Ubuntu (still apt/Ubuntu domain).

---

## High-level Architecture

### Components

1. **Git repo (source of truth)**
   - `flake.nix` pins `nixpkgs`
   - host definitions + reusable modules (roles)

2. **CLI Orchestrator (NixFleet)**
   - reads inventory + host specs
   - evaluates/builds host closures
   - deploys over SSH (no resident agent)

3. **Binary cache (recommended)**
   - speeds up deploys; avoids host builds
   - supports signed artifacts

4. **Hosts**
   - Ubuntu hosts: Nix daemon + sudo + SSH
   - NixOS hosts: standard NixOS switch tooling + SSH

---

## Core Idea

For each host, NixFleet computes a **host closure** and an **activation step**:

1. **Build** host closure (locally/CI)
2. **Copy** closure to host (`nix copy` over SSH)
3. **Activate** on host (sudo) to apply changes

---

## Repository Layout

```text
nixfleet/
  flake.nix
  inventory/
    prod.yaml
  hosts/
    web-1.nix
    db-1.nix
    gpu-1.nix
  modules/
    base.nix
    users.nix
    files.nix
    systemd.nix
    nginx.nix
    myapp.nix
    os-updates-ubuntu.nix
  backends/
    ubuntu/
      compile.nix
      activate.nix
    nixos/
      compile.nix
  secrets/
    prod/
      web-1.age
```

---

## Inventory & Host Spec

Inventory should include:
- address, SSH user, base (`ubuntu` or `nixos`)
- roles
- OS update policy
- rollout policy (canary %, max parallel, reboot windows)

Example (conceptual):

```nix
hosts = {
  web-1 = {
    base = "ubuntu";
    addr = "10.0.1.11";
    roles = [ "nginx" "myapp" ];
    osUpdates.ubuntu = {
      mode = "security-daily";      # "full-weekly" | "manual"
      autoReboot = true;
      rebootWindow = "Sun 02:00-04:00";
      holds = [ "nvidia-driver-*" ]; # optional
      maxConcurrentReboots = 1;
    };
  };

  gpu-1 = {
    base = "nixos";
    addr = "10.0.9.50";
    roles = [ "cuda-box" "inference" ];
    osUpdates.nixos = { autoSwitch = true; };
  };
};
```

---

## CLI UX (Ansible-like)

- `nixfleet plan --host web-1`
- `nixfleet apply --group web --parallel 20`
- `nixfleet rollback --host web-1 --to previous`
- `nixfleet status --group all`
- `nixfleet os-update --group ubuntu --strategy canary`
- `nixfleet run --host web-1 -- task:rotate-logs`

---

## Apply Pipeline (Common)

Each apply is stage-based:

1. **Preflight**
   - SSH connectivity, sudo permissions
   - disk space for `/nix` + `/var`
   - ensure Nix daemon is running (Ubuntu) / nix-store OK (NixOS)
   - optional: host fingerprint checks

2. **OS Updates (optional / policy-driven)**
   - Ubuntu: apt upgrade per policy; detect reboot requirement
   - NixOS: handled via generation switch (if you update flake inputs)

3. **Desired-State (Nix)**
   - evaluate/build closure
   - `nix copy` to host
   - backend-specific activation

4. **Health Checks**
   - systemd unit status
   - HTTP probes / command probes
   - automatic stop/rollback rules (policy)

---

## Unifying Concept: Intent Modules

Write backend-agnostic “intent” modules describing:
- required packages
- `/etc` files content/permissions
- systemd units/timers
- users/groups/directories
- optional firewall rules and health checks

Then compile intent into:
- **Ubuntu backend**: Nix profile + `/etc` payload + unit files + `activate` script
- **NixOS backend**: NixOS module config (native `nixosConfigurations.<host>`)

Example intent snippet:

```nix
{
  nixfleet.packages = [ pkgs.nginx pkgs.git ];

  nixfleet.files."/etc/myapp/config.json" = {
    text = builtins.toJSON { port = 8080; };
    mode = "0640";
    owner = "root";
    group = "myapp";
    restartUnits = [ "myapp.service" ];
  };

  nixfleet.systemd.units."myapp.service" = {
    text = ''
      [Unit]
      After=network-online.target

      [Service]
      ExecStart=${pkgs.myapp}/bin/myapp --config /etc/myapp/config.json
      Restart=always

      [Install]
      WantedBy=multi-user.target
    '';
    enabled = true;
  };

  nixfleet.users.myapp = { system = true; uid = 991; group = "myapp"; home = "/var/lib/myapp"; };
}
```

---

## Backend: Ubuntu (Nix-on-Ubuntu)

### What NixFleet Manages on Ubuntu

- **Pinned Nix profile** for packages/runtime deps:
  - `/nix/var/nix/profiles/nixfleet/system -> generation N`
- Selected `/etc` files (only those managed by NixFleet)
- systemd units/timers under `/etc/systemd/system`
- users/groups/directories (via idempotent activation)

### Activation Algorithm (Ubuntu)

On `apply`:
1. Install/advance NixFleet system profile generation
2. Stage `/etc` payload (e.g., `/etc/.nixfleet/staging/...`)
3. Atomically update managed files only
4. Install/update unit files
5. `systemctl daemon-reload`
6. Restart only impacted units (based on `restartUnits` mapping)
7. Write state: `/var/lib/nixfleet/state.json`

### Rollback Semantics (Ubuntu)

- Roll back **Nix-managed parts** (profile + `/etc` payload + units)
- apt-level changes are forward-only unless you add snapshotting (ZFS/Btrfs/LVM/EBS snapshots)

---

## Backend: NixOS

For NixOS hosts:
- Host output: `nixosConfigurations.<host>`
- Deploy:
  1. `nix copy` system closure
  2. run `switch-to-configuration switch` remotely
- Rollback:
  - native generations (switch back to previous generation)
- Reboot:
  - used when kernel/initrd changes or policy requires

---

## Ubuntu OS Updates: First-Class Support

### Update Policies

- `security-daily`:
  - install security updates regularly
  - reboots only when required and within the window (if enabled)
- `full-weekly`:
  - full upgrade during a maintenance window
- `manual`:
  - never touch apt; only report pending updates/reboot status

### Implementation Options

**Option A: Configure unattended-upgrades (recommended baseline)**
- NixFleet ensures unattended-upgrades is configured and timers enabled
- NixFleet monitors outcomes + reboots as policy permits

**Option B: Orchestrated apt runs (Ansible-like “do updates now”)**
- `apt-get update`
- upgrade security-only or full
- record package diffs
- detect reboot requirement and coordinate reboot

### Holds / Pinning / Guardrails

- package holds (e.g., kernel or GPU driver holds)
- phased rollouts: canary → 10% → 50% → 100%
- `maxConcurrentReboots` per group
- maintenance windows per host/group

---

## Reboot Orchestration

NixFleet treats reboot as an explicit state transition:

1. Detect reboot-needed (Ubuntu: `/var/run/reboot-required`)
2. If reboot needed and allowed by policy/window:
   - run pre-reboot hooks (optional)
     - e.g., drain node in k8s, remove from load balancer
   - reboot
   - wait for SSH to return
   - run post-reboot hooks (optional)
     - e.g., uncordon node, re-add to LB
   - run health checks; halt rollout if unhealthy

---

## Secrets Handling

Hard rule: **no secrets in `/nix/store`**.

Recommended pipeline:
- keep secrets encrypted in git (`age`/`sops`)
- decrypt on deploy machine/CI with tight access controls
- stream to host into:
  - `/run/nixfleet-secrets/...` (tmpfs) or root-only directory
- activation fixes ownership/permissions and restarts dependent units

---

## State, Drift Detection, and Plan

Each host maintains:

`/var/lib/nixfleet/state.json`
- last applied Nix generation + manifest hash
- last OS update timestamp + diff summary
- reboot-needed flag
- service health snapshot

`nixfleet plan` computes:
- desired manifest vs host state
- file diffs for managed `/etc`
- which units would restart
- (Ubuntu) pending apt updates + whether a reboot is expected

`nixfleet status` reports:
- last apply time/generation
- unit health
- reboot-needed state
- (optional) drift: detect out-of-band edits to managed files

---

## Roadmap / Implementation Steps

1. **Bootstrap**
   - install Nix (daemon) on Ubuntu hosts
   - create `deploy` user + sudoers rules

2. **Ubuntu backend MVP**
   - packages + `/etc` payload + systemd units + idempotent activate
   - manifest hashing + restart minimization

3. **NixOS backend MVP**
   - deploy + switch + rollback integration

4. **OS updates MVP**
   - unattended-upgrades config + status reporting
   - orchestrated `os-update` command with canary rollout
   - reboot coordinator with windows and concurrency limits

5. **Binary cache + signing**
6. **Secrets pipeline**
7. **Advanced drift detection + reporting dashboards**

---

## Operational Notes

- Prefer building in CI + using a binary cache for speed and consistency.
- Use health checks and canaries for both apt updates and service config rollouts.
- For “strong rollback” on Ubuntu, pair apt updates with filesystem snapshots (where possible).

---

## Summary

NixFleet provides:
- **Ansible-like orchestration** with **Nix-built deterministic artifacts**
- **Dual backend support** for **Ubuntu** (partial declarative control) and **NixOS** (full declarative control)
- **Safe OS update management** on Ubuntu with reboot coordination and rollout strategy

# NixFleet

Fleet management CLI for deploying Nix configurations over SSH, to hosts that
are not running NixOS (Ubuntu, DGX OS) as well as to NixOS hosts.

## Features

- **Multi-platform support**: Deploy to Ubuntu, DGX OS, and NixOS hosts, plus
  Synology NAS over the DSM API. macOS hosts (`base = "darwin"`, via
  nix-darwin) are supported for a deliberately narrower set of options —
  packages, `/etc` files, users, groups, directories and hooks. Systemd units,
  secrets and apt are **not** supported there and the backend refuses them by
  name at evaluation time rather than dropping them. See
  [docs/darwin.md](docs/darwin.md).
- **k0s Kubernetes**: Bootstrap and manage k0s clusters with Cilium CNI
- **Fleet PKI**: Built-in CA for TLS certificates across your fleet
- **Gateway API**: Shared ingress gateway with auto-generated certificates
- **GitOps pull mode**: Hosts automatically pull and apply configurations
- **Age-encrypted secrets**: SSH host key integration for zero-config decryption
- **Declarative configuration**: Define packages, files, users, systemd units via Nix
- **Health checks**: Monitor host health with configurable checks
- **Resource reconciliation**: Automatic cleanup of orphaned k0s resources
- **Web UI**: Dashboard for fleet visibility and management

## Installation

### Homebrew (macOS/Linux)

```bash
brew tap zach-source/tap
brew install nixfleet
```

### Nix

```bash
nix profile install github:zach-source/nix-fleet
```

### From Source

```bash
git clone https://github.com/zach-source/nix-fleet.git
cd nix-fleet
nix develop
go build -o nixfleet ./cmd/nixfleet
```

## Quick Start

### 1. Bootstrap a new host

```bash
# On the target host
curl -sSL https://raw.githubusercontent.com/zach-source/nix-fleet/main/scripts/bootstrap-ubuntu.sh | \
  sudo bash -s -- --deploy-user nixbot --ssh-key "ssh-ed25519 AAAA..."
```

### 2. Create inventory

```yaml
# inventory/fleet.yaml  (see inventory/example.yaml for groups, roles,
# os_updates and the per-host ssh_key pin)
hosts:
  myhost:
    base: ubuntu
    addr: myhost.local
    ssh_user: nixbot
```

### 3. Create host configuration

Everything lives under the `nixfleet.` prefix, and `nixfleet.host` is required:

```nix
# hosts/myhost.nix
{ pkgs, ... }:
{
  nixfleet = {
    host = {
      name = "myhost";
      base = "ubuntu";
      addr = "myhost.local";
    };

    packages = with pkgs; [
      htop
      vim
      git
    ];

    files."/etc/motd".text = "Welcome to myhost!";
  };
}
```

Then register it in `flake.nix` under `nixfleetConfigurations`:

```nix
myhost = mkNixFleetConfiguration {
  modules = [ ./hosts/myhost.nix ];
};
```

### 4. Deploy

```bash
nixfleet apply -H myhost
```

## Commands

### Core Commands

| Command | Description |
|---------|-------------|
| `nixfleet plan` | Preview changes without applying |
| `nixfleet apply` | Apply configuration to hosts |
| `nixfleet status` | Show host status and health |
| `nixfleet rollback` | Rollback to previous generation |

### Host Management

| Command | Description |
|---------|-------------|
| `nixfleet host onboard` | Onboard a new host (get age key, setup secrets, install pull mode) |

### Secrets Management

| Command | Description |
|---------|-------------|
| `nixfleet secrets rekey` | Re-encrypt all secrets after modifying secrets.nix |
| `nixfleet secrets edit` | Edit a secret in-place |
| `nixfleet secrets add` | Add a new encrypted secret |
| `nixfleet secrets host-key` | Get age public key from SSH host key |

### Pull Mode (GitOps)

| Command | Description |
|---------|-------------|
| `nixfleet pull-mode install` | Install pull mode on hosts |
| `nixfleet pull-mode status` | Show pull mode status |
| `nixfleet pull-mode trigger` | Trigger immediate pull |
| `nixfleet pull-mode uninstall` | Remove pull mode from hosts |

### k0s Kubernetes

| Command | Description |
|---------|-------------|
| `nixfleet k0s init` | Bootstrap k0s controller |
| `nixfleet k0s status` | Show cluster status, nodes, Helm releases |
| `nixfleet k0s kubeconfig` | Get kubeconfig for cluster access |
| `nixfleet k0s certmanager` | Deploy Fleet CA to cert-manager |

### PKI Management

| Command | Description |
|---------|-------------|
| `nixfleet pki init` | Initialize Fleet PKI (root + intermediate CA) |
| `nixfleet pki issue` | Issue host certificates |
| `nixfleet pki trust` | Export CA for trust distribution |
| `nixfleet pki status` | Show PKI status and certificate info |

### Other Commands

| Command | Description |
|---------|-------------|
| `nixfleet server` | Start the web UI and API server |
| `nixfleet os-update` | Manage OS package updates |
| `nixfleet reboot` | Orchestrate host reboots |
| `nixfleet drift` | Detect and fix configuration drift |
| `nixfleet run` | Run ad-hoc commands on hosts |
| `nixfleet state` | Inspect and manage NixFleet host state |
| `nixfleet agents` | Manage fleet agents |
| `nixfleet cache` | Manage the binary cache |
| `nixfleet nix` | Manage the fleet's flake inputs (nixpkgs) |
| `nixfleet juicefs` | Bootstrap and manage the shared JuiceFS filesystem |
| `nixfleet spire` | SPIRE identity management for this host |
| `nixfleet synology` | Manage a Synology NAS via the DSM API (Model B) |
| `nixfleet node-status` | Run a node status HTTP server (for pull-mode nodes) |

`nixfleet <command> --help` is authoritative; this table is a summary.

## Secrets Management

NixFleet uses age encryption with SSH host key integration for secrets:

```bash
# Get a host's age public key
nixfleet secrets host-key myhost

# Add to secrets/secrets.nix
cat > secrets/secrets.nix << 'EOF'
let
  admins = {
    alice = "age1...";
  };
  hosts = {
    myhost = "age1...";
  };
in {
  "api-key.age".publicKeys = builtins.attrValues admins ++ [ hosts.myhost ];
}
EOF

# Create a secret
echo "secret-value" | nixfleet secrets add api-key --host myhost

# Re-encrypt after adding hosts
nixfleet secrets rekey
```

Secrets are automatically decrypted on hosts using their SSH host key - no manual key distribution required.

## Pull Mode (GitOps)

Enable GitOps-style deployments where hosts pull their own configurations:

```bash
# Install pull mode
nixfleet pull-mode install -H myhost --repo git@github.com:org/fleet-config.git

# Hosts will automatically:
# 1. Pull from git on a timer (nixfleet.pullMode.interval, default 15min)
# 2. Build the Nix configuration
# 3. Apply changes
# 4. Report status via webhook (optional)
```

Disabling `pullMode` in a host config does **not** remove the units that were
already installed — they stay and keep failing on their timer. Use
`nixfleet pull-mode uninstall` to take them off the host.

## k0s Kubernetes

NixFleet can bootstrap and manage k0s Kubernetes clusters with Cilium CNI. The
controller is bootstrapped by the CLI (`nixfleet k0s init`); the declarative
surface is for **workers**, via `modules/k0s.nix`:

```nix
# hosts/myworker.nix
{
  imports = [ ../modules/k0s.nix ];

  nixfleet.k0s.worker = {
    enable = true;

    # Memory held back from kubelet, so pods cannot evict the inference
    # processes that are not Kubernetes workloads. On a 122 GiB box, 78Gi
    # leaves ~44 GiB allocatable.
    systemReservedMemory = "78Gi";

    # Optional
    version = "v1.34.2+k0s.0";
    tokenPath = "/etc/k0s/worker-join-token";
    extraKubeletArgs = [ "--max-pods=200" ];
  };
}
```

Cilium LoadBalancer, Gateway API and cert-manager are deployed by the CLI
during `nixfleet k0s init` / `nixfleet k0s certmanager`, not declared as Nix
options.

### Bootstrap k0s

```bash
# Initialize PKI (once per fleet)
nixfleet pki init

# Deploy to controller
nixfleet apply -H k8s-controller

# Bootstrap k0s
nixfleet k0s init -H k8s-controller

# Deploy Fleet CA to cert-manager
nixfleet k0s certmanager -H k8s-controller

# Get kubeconfig
nixfleet k0s kubeconfig -H k8s-controller > ~/.kube/config
```

### Deploy Applications

Applications attach HTTPRoutes to the shared gateway:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-app
  namespace: my-namespace
spec:
  parentRefs:
  - name: default-ingress-gateway
    namespace: cilium-gateway
  hostnames:
  - myapp.example.com
  rules:
  - backendRefs:
    - name: my-service
      port: 80
```

## Configuration Reference

### Host Configuration Options

```nix
{
  nixfleet = {
    # Identity. Required.
    host = {
      name = "myhost";
      # "ubuntu" | "nixos" | "dgx" | "synology". "dgx" deploys exactly like
      # "ubuntu" but is never OS-updated; "synology" is driven over the DSM
      # API rather than SSH.
      base = "ubuntu";
      addr = "myhost.local";
    };

    # Packages to install via Nix. Hosts list only their extras — the
    # fleet-wide set comes from importing modules/base-packages.nix.
    packages = [ pkgs.htop pkgs.vim ];

    # Files to deploy. `text` or `source`, plus optional restartUnits.
    files."/etc/myconfig".text = "content";
    files."/etc/other".source = ./other;

    # Directories to create
    directories."/var/lib/myapp" = {
      owner = "myuser";
      group = "mygroup";
      mode = "0750";
    };

    # Users and groups
    users.myuser = {
      uid = 1001;
      group = "mygroup";
      home = "/home/myuser";
      shell = "/bin/bash";
    };

    groups.mygroup.gid = 1001;

    # Systemd units. NixFleet writes unit *text* verbatim — there is no
    # systemd.services submodule with serviceConfig, as in NixOS.
    systemd.units."myservice.service" = {
      enabled = true;
      text = ''
        [Unit]
        Description=My Service

        [Service]
        ExecStart=/usr/bin/myapp
        Restart=always

        [Install]
        WantedBy=multi-user.target
      '';
    };

    # Health checks: an attribute set keyed by check name, not a list.
    healthChecks.http = {
      type = "http";
      url = "http://localhost:8080/health";
      timeout = 10;
    };

    # Secrets (age-encrypted)
    secrets.items.api-key = {
      source = ../secrets/api-key.age;
      path = "/run/nixfleet-secrets/api-key";
      owner = "root";
      mode = "0400";
    };
  };
}
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Control Plane                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │  nixfleet   │  │   Web UI    │  │  Git Repository     │ │
│  │    CLI      │  │   :8080     │  │  (fleet-config)     │ │
│  └──────┬──────┘  └──────┬──────┘  └──────────┬──────────┘ │
└─────────┼────────────────┼───────────────────┼─────────────┘
          │                │                   │
          │ SSH            │ HTTP              │ Git (pull)
          │                │                   │
┌─────────▼────────────────▼───────────────────▼─────────────┐
│                    Managed Hosts                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │  Ubuntu / macOS Host                                 │   │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐ │   │
│  │  │ Nix Daemon  │  │ Pull Mode   │  │  Secrets    │ │   │
│  │  │             │  │  (systemd)  │  │ (tmpfs)     │ │   │
│  │  └─────────────┘  └─────────────┘  └─────────────┘ │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

## Development

```bash
# Enter development shell
nix develop

# Check that every host in nixfleetConfigurations still evaluates
nix flake check

# Format Nix, from the repo root
nixfmt flake.nix modules/*.nix hosts/*.nix

# The Go module is rooted at cmd/nixfleet, so go commands run from there
cd cmd/nixfleet
go build -o nixfleet .
go test ./...
go fmt ./...
```

## License

MIT License - see [LICENSE](LICENSE) for details.

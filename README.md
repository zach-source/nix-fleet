# NixFleet

Fleet management CLI for deploying Nix configurations to non-NixOS hosts (Ubuntu, macOS).

## Features

- **Multi-platform support**: Deploy to Ubuntu and macOS hosts
- **GitOps pull mode**: Hosts automatically pull and apply configurations
- **Age-encrypted secrets**: SSH host key integration for zero-config decryption
- **Declarative configuration**: Define packages, files, users, systemd units via Nix
- **Health checks**: Monitor host health with configurable checks
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
# inventory/hosts.yaml
hosts:
  myhost:
    name: myhost
    addr: myhost.local
    base: ubuntu
    ssh_user: nixbot
```

### 3. Create host configuration

```nix
# hosts/myhost.nix
{ config, pkgs, ... }:
{
  packages = with pkgs; [
    htop
    vim
    git
  ];

  files."/etc/motd".text = "Welcome to myhost!";
}
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

### Other Commands

| Command | Description |
|---------|-------------|
| `nixfleet server` | Start the web UI and API server |
| `nixfleet os-update` | Manage OS package updates |
| `nixfleet reboot` | Orchestrate host reboots |
| `nixfleet drift` | Detect configuration drift |
| `nixfleet run` | Run ad-hoc commands on hosts |

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
# 1. Pull from git every 5 minutes
# 2. Build the Nix configuration
# 3. Apply changes
# 4. Report status via webhook (optional)
```

## Configuration Reference

### Host Configuration Options

```nix
{
  # Packages to install via Nix
  packages = [ pkgs.htop pkgs.vim ];

  # Files to deploy
  files."/etc/myconfig".text = "content";
  files."/etc/myconfig".source = ./myconfig;

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

  # Systemd services
  systemd.services.myservice = {
    description = "My Service";
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      ExecStart = "/usr/bin/myapp";
      Restart = "always";
    };
  };

  # Health checks
  healthChecks = [
    {
      name = "http";
      type = "http";
      url = "http://localhost:8080/health";
      timeout = 10;
    }
  ];

  # Secrets (age-encrypted)
  secrets.items.api-key = {
    source = ../secrets/api-key.age;
    path = "/run/nixfleet-secrets/api-key";
    owner = "root";
    mode = "0400";
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

# Build
go build -o nixfleet ./cmd/nixfleet

# Run tests
go test ./cmd/nixfleet/...

# Format
go fmt ./cmd/nixfleet/...
nixfmt modules/ lib/ backends/
```

## License

MIT License - see [LICENSE](LICENSE) for details.

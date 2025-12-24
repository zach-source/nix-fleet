# NixOS Host Requirements for NixFleet

This document describes the requirements for managing NixOS hosts with NixFleet.

## Prerequisites

### 1. SSH Access

NixFleet connects to NixOS hosts via SSH. Ensure:

- SSH server is running (`services.openssh.enable = true`)
- Deploy user has SSH access with key-based authentication
- Root or passwordless sudo access for the deploy user

**Example NixOS configuration:**

```nix
{
  # Enable SSH
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "no";
      PasswordAuthentication = false;
    };
  };

  # Deploy user
  users.users.deploy = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    openssh.authorizedKeys.keys = [
      "ssh-ed25519 AAAA... deploy@nixfleet"
    ];
  };

  # Passwordless sudo for deploy user
  security.sudo.extraRules = [{
    users = [ "deploy" ];
    commands = [{
      command = "ALL";
      options = [ "NOPASSWD" ];
    }];
  }];
}
```

### 2. Nix Settings

For remote builds and deployment, configure:

```nix
{
  nix.settings = {
    # Allow remote builds
    trusted-users = [ "deploy" ];

    # Recommended: use binary cache
    substituters = [
      "https://cache.nixos.org"
      "https://your-cache.example.com"  # Your binary cache
    ];
    trusted-public-keys = [
      "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
      "your-cache:..."  # Your cache signing key
    ];
  };
}
```

### 3. Network Requirements

- SSH port accessible from deployment machine (default: 22)
- Access to Nix binary cache (or local builds)
- Outbound access for package downloads (if building locally)

## Deployment Flow

NixFleet deploys to NixOS hosts using:

1. **Evaluate**: Build `nixosConfigurations.<host>` from your flake
2. **Copy**: `nix copy --to ssh://deploy@host` the system closure
3. **Switch**: Run `switch-to-configuration switch` on the host

### Example Commands (Manual)

```bash
# Build the system closure locally
nix build .#nixosConfigurations.myhost.config.system.build.toplevel

# Copy to remote host
nix copy --to ssh://deploy@10.0.1.50 ./result

# Activate on remote host
ssh deploy@10.0.1.50 sudo /nix/store/.../bin/switch-to-configuration switch
```

NixFleet automates this entire flow.

## Integration with NixFleet

### Option A: Separate NixOS Configurations

Define NixOS hosts in your NixFleet flake:

```nix
{
  nixosConfigurations = {
    db-1 = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ./hosts/db-1/configuration.nix
        ./modules/base.nix
        ./modules/postgresql.nix
      ];
    };
  };
}
```

### Option B: Intent Modules (Unified)

Use NixFleet intent modules that compile to NixOS modules:

```nix
# hosts/db-1.nix
{ config, pkgs, ... }:

{
  nixfleet = {
    host = {
      name = "db-1";
      base = "nixos";
      addr = "10.0.2.10";
    };

    packages = [ pkgs.postgresql ];

    # These compile to NixOS module options
    services.postgresql = {
      enable = true;
      package = pkgs.postgresql_16;
    };
  };
}
```

The NixOS backend compiles intent modules to native NixOS configuration.

## Rollback

NixFleet supports native NixOS generations for rollback:

```bash
# Rollback to previous generation
nixfleet rollback --host db-1

# Rollback to specific generation
nixfleet rollback --host db-1 --to 42

# List generations
nixfleet status --host db-1 --generations
```

Internally, this runs:

```bash
# Switch to previous generation
sudo /nix/var/nix/profiles/system-<N>-link/bin/switch-to-configuration switch
```

## Reboot Handling

NixFleet detects when a reboot is needed:

- Kernel version changed
- initrd changed
- Services requiring reboot

Configure reboot policy:

```nix
# In inventory
hosts.db-1 = {
  base = "nixos";
  osUpdates.nixos = {
    autoSwitch = true;
    autoReboot = false;  # Manual reboot required
    # or
    autoReboot = true;
    rebootWindow = "Sun 03:00-05:00";
  };
};
```

## Health Checks

After deployment, NixFleet runs health checks:

```nix
nixfleet.healthChecks = {
  postgresql = {
    type = "command";
    command = "pg_isready -h localhost";
    timeout = 30;
  };

  api = {
    type = "http";
    url = "http://localhost:8080/health";
    expectedStatus = 200;
    timeout = 10;
  };
};
```

If health checks fail, NixFleet can automatically rollback.

## Troubleshooting

### "Permission denied" during deploy

Ensure the deploy user has:
- SSH access
- Passwordless sudo
- Is in `nix.settings.trusted-users`

### "Store path not valid" errors

The closure wasn't fully copied. Check:
- Network connectivity
- Disk space on target
- Binary cache availability

### Boot fails after switch

1. Reboot into previous generation (GRUB menu)
2. Run `nixos-rebuild switch --rollback`
3. Investigate the failed configuration

### Slow deployments

- Use a binary cache to avoid remote builds
- Increase `--parallel` for multi-host deploys
- Consider building in CI and pushing to cache

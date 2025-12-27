# NixFleet module options
# Defines the schema for nixfleet.* configuration options
{
  config,
  lib,
  pkgs,
  ...
}:

with lib;

let
  # File definition type
  fileType = types.submodule (
    { name, ... }:
    {
      options = {
        text = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Text content of the file";
        };

        source = mkOption {
          type = types.nullOr types.path;
          default = null;
          description = "Path to source file";
        };

        mode = mkOption {
          type = types.str;
          default = "0644";
          description = "File permissions (octal)";
        };

        owner = mkOption {
          type = types.str;
          default = "root";
          description = "File owner";
        };

        group = mkOption {
          type = types.str;
          default = "root";
          description = "File group";
        };

        restartUnits = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Systemd units to restart when this file changes";
        };
      };
    }
  );

  # Systemd unit type
  unitType = types.submodule (
    { name, ... }:
    {
      options = {
        text = mkOption {
          type = types.str;
          description = "Unit file content";
        };

        enabled = mkOption {
          type = types.bool;
          default = true;
          description = "Whether the unit should be enabled";
        };
      };
    }
  );

  # User type
  userType = types.submodule (
    { name, ... }:
    {
      options = {
        system = mkOption {
          type = types.bool;
          default = false;
          description = "Whether this is a system user";
        };

        uid = mkOption {
          type = types.nullOr types.int;
          default = null;
          description = "User ID (null for auto-assign)";
        };

        group = mkOption {
          type = types.str;
          default = name;
          description = "Primary group";
        };

        extraGroups = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Additional groups";
        };

        home = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Home directory";
        };

        createHome = mkOption {
          type = types.bool;
          default = false;
          description = "Whether to create the home directory";
        };

        shell = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Login shell";
        };

        description = mkOption {
          type = types.str;
          default = "";
          description = "User description (GECOS)";
        };
      };
    }
  );

  # Group type
  groupType = types.submodule (
    { name, ... }:
    {
      options = {
        gid = mkOption {
          type = types.nullOr types.int;
          default = null;
          description = "Group ID (null for auto-assign)";
        };

        system = mkOption {
          type = types.bool;
          default = false;
          description = "Whether this is a system group";
        };
      };
    }
  );

  # Directory type
  directoryType = types.submodule (
    { name, ... }:
    {
      options = {
        mode = mkOption {
          type = types.str;
          default = "0755";
          description = "Directory permissions (octal)";
        };

        owner = mkOption {
          type = types.str;
          default = "root";
          description = "Directory owner";
        };

        group = mkOption {
          type = types.str;
          default = "root";
          description = "Directory group";
        };
      };
    }
  );

  # Secret type (age-encrypted secrets)
  secretType = types.submodule (
    { name, ... }:
    {
      options = {
        source = mkOption {
          type = types.path;
          description = "Path to the age-encrypted secret file";
        };

        path = mkOption {
          type = types.str;
          description = "Destination path on the host";
        };

        owner = mkOption {
          type = types.str;
          default = "root";
          description = "File owner";
        };

        group = mkOption {
          type = types.str;
          default = "root";
          description = "File group";
        };

        mode = mkOption {
          type = types.str;
          default = "0400";
          description = "File permissions (octal)";
        };

        restartUnits = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Systemd units to restart when this secret changes";
        };
      };
    }
  );

  # Health check type
  healthCheckType = types.submodule (
    { name, ... }:
    {
      options = {
        type = mkOption {
          type = types.enum [
            "command"
            "http"
            "tcp"
          ];
          description = "Type of health check";
        };

        command = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Command to run (for command type)";
        };

        url = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "URL to check (for http type)";
        };

        host = mkOption {
          type = types.str;
          default = "localhost";
          description = "Host to connect to (for tcp type)";
        };

        port = mkOption {
          type = types.nullOr types.int;
          default = null;
          description = "Port to connect to (for tcp type)";
        };

        expectedStatus = mkOption {
          type = types.int;
          default = 200;
          description = "Expected HTTP status code";
        };

        timeout = mkOption {
          type = types.int;
          default = 30;
          description = "Timeout in seconds";
        };

        interval = mkOption {
          type = types.int;
          default = 10;
          description = "Check interval in seconds";
        };
      };
    }
  );

  # Assertion type (like NixOS)
  assertionType = types.submodule {
    options = {
      assertion = mkOption {
        type = types.bool;
        description = "If false, the assertion fails and the build aborts";
      };
      message = mkOption {
        type = types.str;
        description = "Error message shown when the assertion fails";
      };
    };
  };

in
{
  # Top-level assertions (NixOS-compatible)
  options.assertions = mkOption {
    type = types.listOf assertionType;
    default = [ ];
    description = ''
      List of assertions that must pass for the build to succeed.
      Each assertion has an `assertion` boolean and a `message` string.
    '';
    example = [
      {
        assertion = true;
        message = "Example assertion that always passes";
      }
    ];
  };

  # Top-level warnings (NixOS-compatible)
  options.warnings = mkOption {
    type = types.listOf types.str;
    default = [ ];
    description = ''
      List of warning messages to display during evaluation.
      Warnings do not fail the build but alert the user to potential issues.
    '';
    example = [ "This feature is deprecated" ];
  };

  options.nixfleet = {
    # Host identification
    host = {
      name = mkOption {
        type = types.str;
        description = "Host name";
      };

      base = mkOption {
        type = types.enum [
          "ubuntu"
          "nixos"
        ];
        description = "Base OS type";
      };

      addr = mkOption {
        type = types.str;
        description = "Host address (IP or hostname)";
      };
    };

    # Packages to install
    packages = mkOption {
      type = types.listOf types.package;
      default = [ ];
      description = "Packages to install via Nix profile";
    };

    # Managed files
    files = mkOption {
      type = types.attrsOf fileType;
      default = { };
      description = "Files to manage under /etc or other locations";
    };

    # Systemd units
    systemd = {
      units = mkOption {
        type = types.attrsOf unitType;
        default = { };
        description = "Systemd unit files to manage";
      };
    };

    # Users
    users = mkOption {
      type = types.attrsOf userType;
      default = { };
      description = "Users to manage";
    };

    # Groups
    groups = mkOption {
      type = types.attrsOf groupType;
      default = { };
      description = "Groups to manage";
    };

    # Directories
    directories = mkOption {
      type = types.attrsOf directoryType;
      default = { };
      description = "Directories to create and manage";
    };

    # Health checks
    healthChecks = mkOption {
      type = types.attrsOf healthCheckType;
      default = { };
      description = "Health checks to run after activation";
    };

    # Secrets (age-encrypted)
    secrets = {
      # Secret definitions
      items = mkOption {
        type = types.attrsOf secretType;
        default = { };
        description = "Age-encrypted secrets to deploy";
      };

      # Decryption mode: use SSH host key (recommended) or explicit age key
      mode = mkOption {
        type = types.enum [
          "ssh-host-key"
          "age-key"
        ];
        default = "ssh-host-key";
        description = ''
          How to derive the age decryption identity:
          - ssh-host-key: Derive from SSH host ed25519 key (recommended, no manual key management)
          - age-key: Use an explicit age private key file
        '';
      };

      # Path to SSH host key (for ssh-host-key mode)
      sshHostKeyPath = mkOption {
        type = types.path;
        default = "/etc/ssh/ssh_host_ed25519_key";
        description = "Path to SSH host ed25519 private key (used when mode = ssh-host-key)";
      };

      # Path to age key (for age-key mode, backward compatible)
      ageKeyPath = mkOption {
        type = types.nullOr types.path;
        default = null;
        description = "Path to age private key file (used when mode = age-key)";
      };

      # Directory where decrypted secrets are stored
      secretsDir = mkOption {
        type = types.path;
        default = "/run/nixfleet-secrets";
        description = "Directory where decrypted secrets are stored";
      };
    };

    # Backward compatibility alias
    ageKeyPath = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "DEPRECATED: Use secrets.ageKeyPath instead";
    };

    # Hooks
    hooks = {
      preActivate = mkOption {
        type = types.lines;
        default = "";
        description = "Script to run before activation";
      };

      postActivate = mkOption {
        type = types.lines;
        default = "";
        description = "Script to run after activation";
      };
    };

    # Pull Mode - GitOps-style self-updating deployments
    pullMode = {
      enable = mkEnableOption "pull-based deployment mode";

      repoURL = mkOption {
        type = types.str;
        description = "Git repository URL (SSH format: git@github.com:org/repo.git)";
        example = "git@github.com:myorg/nix-fleet-hosts.git";
      };

      branch = mkOption {
        type = types.str;
        default = "main";
        description = "Git branch to track";
      };

      sshKeyPath = mkOption {
        type = types.str;
        default = "/run/nixfleet-secrets/github-deploy-key";
        description = "Path to SSH key for Git access";
      };

      interval = mkOption {
        type = types.str;
        default = "15min";
        description = "Pull interval (systemd timer format)";
      };

      applyOnBoot = mkOption {
        type = types.bool;
        default = true;
        description = "Apply configuration on boot";
      };

      repoPath = mkOption {
        type = types.str;
        default = "/var/lib/nixfleet/repo";
        description = "Local path to clone repository";
      };

      webhook = {
        url = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Webhook URL for status notifications";
        };

        secret = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Webhook secret for signing (set via secrets, not here)";
        };
      };

      homeManager = {
        enable = mkEnableOption "home-manager integration in pull mode";

        user = mkOption {
          type = types.str;
          description = "Username to run home-manager as";
        };

        dotfilesPath = mkOption {
          type = types.str;
          description = "Path to dotfiles repository on the host";
        };

        branch = mkOption {
          type = types.str;
          default = "main";
          description = "Branch to track for dotfiles";
        };

        sshKeyPath = mkOption {
          type = types.str;
          default = "";
          description = "Path to SSH key for dotfiles Git access";
        };

        configName = mkOption {
          type = types.str;
          description = "Flake configuration name (e.g., 'user@x86_64-linux')";
        };
      };
    };
  };
}

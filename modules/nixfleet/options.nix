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

in
{
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
  };
}

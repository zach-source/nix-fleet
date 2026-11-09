# NixFleet Dolt Module
#
# Runs a self-hosted Dolt server that exposes BOTH:
#   - the MySQL-compatible SQL port (`port`, default 3306) for queries, and
#   - the gRPC remotes API (`remotesApiPort`, default 50051) for
#     `dolt clone | push | pull` against a remote like
#     `https://<host>:<remotesApiPort>/<owner>/<db>`.
#
# Wraps `dolt sql-server`, which serves both ports from one process and reads
# its config from a YAML file. Module owns:
#   - a system user/group for the daemon
#   - the data directory under `/srv/dolt` (parent for dolt databases)
#   - /etc/dolt/server.yaml (the daemon config)
#   - dolt-server.service (systemd unit)
#   - a healthcheck
#
# Storage management (e.g. ZFS quotas) is intentionally NOT handled here so the
# module stays portable — set up the dataDir filesystem in the host config
# (see zach-source/nix-fleet-hosts hosts/gti.nix for an example with a ZFS
# dataset + 100G quota; gti's host config lives there now).
#
# Usage:
#   imports = [ ../modules/dolt.nix ];
#   nixfleet.modules.dolt = {
#     enable = true;
#     # all options have sensible defaults; override per-host as needed
#   };
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.nixfleet.modules.dolt;

  yaml = pkgs.formats.yaml { };
  configFile = yaml.generate "dolt-server.yaml" {
    log_level = cfg.logLevel;
    behavior = {
      read_only = cfg.readOnly;
      autocommit = true;
    };
    user = {
      name = cfg.username;
      password = cfg.password;
    };
    listener = {
      host = cfg.address;
      port = cfg.port;
      read_timeout_millis = 28800000;
      write_timeout_millis = 28800000;
    };
    data_dir = cfg.dataDir;
    cfg_dir = "${cfg.dataDir}/.doltcfg";
    remotesapi = {
      port = cfg.remotesApiPort;
      read_only = cfg.readOnly;
    };
  };

  # IF NOT EXISTS preserves passwords on already-provisioned accounts.
  remoteGrants = pkgs.writeText "dolt-remote-grants.sql" "
    CREATE USER IF NOT EXISTS 'root'@'%' IDENTIFIED BY '${
        lib.replaceStrings [ "\\" "'" ] [ "\\\\" "''" ] cfg.password
      }';
    GRANT CLONE_ADMIN ON *.* TO 'root'@'%';
    GRANT ALL PRIVILEGES ON *.* TO 'root'@'%' WITH GRANT OPTION;
  ";
  grantScript = pkgs.writeShellScript "dolt-ensure-remote-grants" ''
    export HOME=${lib.escapeShellArg cfg.dataDir}
    cd ${lib.escapeShellArg cfg.dataDir}
    ${cfg.package}/bin/dolt --data-dir ${lib.escapeShellArg cfg.dataDir} sql < ${remoteGrants} \
      >/dev/null 2>&1 || echo "dolt: remotesapi grant provisioning failed (non-fatal)" >&2
  '';
  systemdExecArg =
    (import "${pkgs.path}/nixos/lib/utils.nix" { inherit lib config pkgs; }).escapeSystemdExecArg;
  serverConfig =
    if cfg.serverConfigFile != null then
      systemdExecArg cfg.serverConfigFile
    else
      "/etc/dolt/server.yaml";

in
{
  options.nixfleet.modules.dolt = {
    enable = lib.mkEnableOption "Dolt SQL + remotes-API server";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.dolt;
      defaultText = lib.literalExpression "pkgs.dolt";
      description = "dolt package to use";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "dolt";
      description = "Unix user the dolt server runs as";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "dolt";
      description = "Unix group the dolt server runs as";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "/srv/dolt";
      description = ''
        Directory holding dolt databases. Each subdirectory is a separate
        `dolt init`'d repo; the server exposes all of them. Provision this
        filesystem in the host config (a plain directory works; ZFS dataset
        with quota is recommended for production).
      '';
    };

    address = lib.mkOption {
      type = lib.types.str;
      default = "0.0.0.0";
      description = ''
        Bind address for BOTH the SQL port and the remotes API port.
        Use "127.0.0.1" for host-only, "0.0.0.0" for LAN/cluster.
      '';
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 3306;
      description = "MySQL-compatible SQL port";
    };

    remotesApiPort = lib.mkOption {
      type = lib.types.port;
      default = 50051;
      description = ''
        gRPC remotes API port. Clients reach this via
        `dolt remote add <name> https://<host>:<port>/<owner>/<db>` and then
        `dolt push | pull | clone`.
      '';
    };

    username = lib.mkOption {
      type = lib.types.str;
      default = "root";
      description = "Default SQL+remotes auth username";
    };

    password = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = ''
        Legacy password (public in the Nix store). Use serverConfigFile to migrate.
        Empty allows password-less login —
        only safe when `address` is restricted (loopback or LAN with firewall).
      '';
    };

    serverConfigFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Absolute runtime path to a complete Dolt server YAML configuration,
        readable only by the service user (e.g. 0600 dolt:dolt). Its contents
        are never read by Nix. Overrides generated server settings; disable
        provisionRemoteRoot and manage SQL accounts separately when using it.
      '';
    };

    provisionRemoteRoot = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Legacy remote root provisioning, retained for compatibility. Creates
        root@% with password only if absent; never changes existing passwords.
        Disable after explicitly provisioning authenticated accounts and
        CLONE_ADMIN grants. Existing accounts and grants are left untouched.
      '';
    };

    readOnly = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "If true, server rejects all writes (including pushes).";
    };

    logLevel = lib.mkOption {
      type = lib.types.enum [
        "trace"
        "debug"
        "info"
        "warning"
        "error"
        "fatal"
      ];
      default = "info";
      description = "dolt-server log level";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.serverConfigFile == null || (!cfg.provisionRemoteRoot && cfg.password == "");
        message = "dolt.serverConfigFile requires provisionRemoteRoot = false and no inline password";
      }
      {
        assertion =
          cfg.serverConfigFile == null
          || (
            lib.hasPrefix "/" cfg.serverConfigFile
            && !(lib.hasPrefix "/nix/store/" cfg.serverConfigFile)
            && !(builtins.hasContext cfg.serverConfigFile)
          );
        message = "dolt.serverConfigFile must be an absolute runtime path outside the Nix store";
      }
    ];
    nixfleet = {
      packages = [ cfg.package ];

      # The NixFleet ubuntu activate script runs Step 2/3 "groups" and "users"
      # before Step 4 "directories". This order applies since nix-fleet#24,
      # 7b69aae. A directory can now declare `owner = cfg.user`. The owner
      # value stays correct. When Step 4 runs, useradd has already created
      # ${cfg.user}.
      #
      # This is the durable fix for dataDir ownership. The old design left
      # dataDir at root:root. It relied on dolt-server.service's
      # ExecStartPre=+chown to repair the owner at service-start time. That
      # chown runs only when the unit (re)starts.
      #
      # Each `nixfleet apply` between restarts set the owner to root:root
      # again. A root-owned directory blocks `mkdir` for a new dolt database.
      # Existing databases, however, stayed readable and writable. The extra
      # o+x in 0751 allows dolt to traverse in.
      #
      # When you declare ownership here, every apply leaves dataDir
      # dolt-owned, not only every restart.
      groups.${cfg.group} = {
        system = true;
      };

      users.${cfg.user} = {
        system = true;
        group = cfg.group;
        home = cfg.dataDir;
        shell = "/usr/sbin/nologin";
        description = "Dolt server";
      };

      directories.${cfg.dataDir} = {
        # 0750: only ${cfg.user}:${cfg.group} need access. dolt-server and
        # dolt-ensure-gascity-backup-db-style helper units both run as
        # User=${cfg.user}, and no other user reads dataDir.
        mode = "0750";
        owner = cfg.user;
        group = cfg.group;
      };

      directories."/etc/dolt" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };

      files = lib.optionalAttrs (cfg.serverConfigFile == null) {
        "/etc/dolt/server.yaml" = {
          text = builtins.readFile configFile;
          mode = "0640";
          owner = cfg.user;
          group = cfg.group;
        };
      };

      systemd.units."dolt-server.service" = {
        text = ''
          [Unit]
          Description=Dolt SQL + remotes-API server
          After=network-online.target
          Wants=network-online.target

          [Service]
          Type=simple
          User=${cfg.user}
          Group=${cfg.group}
          WorkingDirectory=${cfg.dataDir}
          # ExecStartPre runs as root (+ prefix). It repairs ownership of
          # subdirectories under dataDir, one per dolt database, that the
          # directories step's top-level chown does not reach. The top-level
          # directory itself is already dolt:dolt (see modules/dolt.nix).
          ExecStartPre=+/bin/chown -R ${cfg.user}:${cfg.group} ${cfg.dataDir}
          # Ensure remotesapi grants exist (see grantScript). Runs as ${cfg.user}
          # (no + prefix); server isn't started yet, so the offline `dolt sql`
          # writes privileges.db without a lock conflict.
          ${lib.optionalString cfg.provisionRemoteRoot "ExecStartPre=${grantScript}"}
          ExecStart=${cfg.package}/bin/dolt sql-server --config ${serverConfig}
          Restart=on-failure
          RestartSec=5

          # Hardening
          NoNewPrivileges=true
          ProtectSystem=strict
          ProtectHome=true
          PrivateTmp=true
          ReadWritePaths=${cfg.dataDir}

          [Install]
          WantedBy=multi-user.target
        '';
        enabled = true;
      };

      healthChecks.dolt-server = {
        type = "command";
        command = "systemctl is-active dolt-server.service";
        timeout = 5;
      };
    };
  };
}

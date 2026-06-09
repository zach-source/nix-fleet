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
# (see hosts/gti.nix for an example with a ZFS dataset + 100G quota).
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
        Default SQL+remotes auth password. Empty allows password-less login —
        only safe when `address` is restricted (loopback or LAN with firewall).
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
    nixfleet = {
      packages = [ cfg.package ];

      # NixFleet's activation runs `directories` BEFORE `users`, so a directory
      # declared with `owner = cfg.user` would fail with
      #   chown: invalid user: '<user>:<group>'
      # on the very first apply. Sidestep by managing the user/group
      # imperatively in the preActivate hook below — idempotent so subsequent
      # applies are no-ops, and reusing the same name across module updates
      # keeps the UID stable.
      hooks.preActivate = lib.mkBefore ''
        if ! getent group ${cfg.group} >/dev/null 2>&1; then
          groupadd --system ${cfg.group}
        fi
        if ! getent passwd ${cfg.user} >/dev/null 2>&1; then
          useradd --system --gid ${cfg.group} \
                  --home-dir ${cfg.dataDir} --no-create-home \
                  --shell /usr/sbin/nologin -c "Dolt server" ${cfg.user}
        fi
      '';

      directories.${cfg.dataDir} = {
        mode = "0750";
        owner = cfg.user;
        group = cfg.group;
      };

      directories."/etc/dolt" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };

      files."/etc/dolt/server.yaml" = {
        text = builtins.readFile configFile;
        mode = "0640";
        owner = cfg.user;
        group = cfg.group;
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
          ExecStart=${cfg.package}/bin/dolt sql-server --config /etc/dolt/server.yaml
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

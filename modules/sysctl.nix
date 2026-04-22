# NixFleet sysctl module
#
# Declarative kernel parameters rendered to /etc/sysctl.d/ and applied
# immediately on deploy via a systemd oneshot + path unit. Ubuntu's
# systemd-sysctl.service picks up /etc/sysctl.d/*.conf on boot, so
# settings persist across restarts without any extra work.
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.sysctl;

  renderValue = v: if builtins.isBool v then (if v then "1" else "0") else toString v;

  # Sort keys for deterministic file content (stable diffs).
  sortedKeys = lib.sort (a: b: a < b) (builtins.attrNames cfg.settings);

  renderedLines = map (k: "${k} = ${renderValue cfg.settings.${k}}") sortedKeys;
in
{
  options.nixfleet.modules.sysctl = {
    enable = lib.mkEnableOption "NixFleet-managed sysctl parameters";

    settings = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.oneOf [
          lib.types.int
          lib.types.str
          lib.types.bool
        ]
      );
      default = { };
      example = {
        "fs.inotify.max_user_watches" = 524288;
        "fs.inotify.max_user_instances" = 8192;
      };
      description = ''
        sysctl key-value pairs written to /etc/sysctl.d/99-nixfleet.conf.
        Applied immediately via a systemd oneshot when the file changes;
        persists across reboots via Ubuntu's systemd-sysctl.service.
      '';
    };

    dropInName = lib.mkOption {
      type = lib.types.str;
      default = "99-nixfleet.conf";
      description = ''
        Filename under /etc/sysctl.d/. The 99- prefix makes it override
        earlier drop-ins (00-distro, 10-*, etc.).
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.files = {
      "/etc/sysctl.d/${cfg.dropInName}" = {
        mode = "0644";
        owner = "root";
        group = "root";
        text = ''
          # Managed by NixFleet — hosts/<host>.nix modules.sysctl.settings
          ${lib.concatStringsSep "\n" renderedLines}
        '';
      };
    };

    nixfleet.systemd.units = {
      "nixfleet-sysctl-apply.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Apply NixFleet-managed sysctl drop-ins
          After=systemd-sysctl.service
          ConditionPathExists=/etc/sysctl.d/${cfg.dropInName}

          [Service]
          Type=oneshot
          # --system re-reads ALL drop-ins; safe to run on any change.
          ExecStart=/sbin/sysctl --system
          RemainAfterExit=yes

          [Install]
          WantedBy=multi-user.target
        '';
      };

      "nixfleet-sysctl-apply.path" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Watch /etc/sysctl.d/${cfg.dropInName} for changes

          [Path]
          PathChanged=/etc/sysctl.d/${cfg.dropInName}
          Unit=nixfleet-sysctl-apply.service

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      nixfleet-sysctl-applied = {
        type = "command";
        command = "systemctl is-active nixfleet-sysctl-apply.service >/dev/null 2>&1 || systemctl show nixfleet-sysctl-apply.service -p Result | grep -q success";
        timeout = 5;
      };
    };
  };
}

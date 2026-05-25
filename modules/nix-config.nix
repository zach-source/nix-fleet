# NixFleet nix-config module
#
# Declaratively manages /etc/nix/nix.custom.conf — the user-config file that
# Determinate Nix's /etc/nix/nix.conf `!include`s on every host. Primarily
# standardizes `trusted-users` across the fleet: the deploy user MUST be a
# trusted-user for `nixfleet apply` to copy (unsigned, locally-built) closures,
# otherwise the daemon rejects them with "lacks a signature by a trusted key".
#
# This was previously unmanaged and inconsistent (gtr-150 had only root, gtr-152
# had no file at all, gtr-153 had duplicate conflicting lines), so deploys
# failed unpredictably. Owning the file makes it uniform.
#
# nix-daemon reads trusted-users at startup, so a path unit restarts it when the
# file changes (same apply-on-change pattern as modules/sysctl.nix).
#
# Bootstrap note: a host that doesn't yet trust `deploy` can't receive this via
# `nixfleet apply` (the closure copy fails first). Such a host needs a one-time
# manual seed (append `extra-trusted-users = deploy` to nix.custom.conf +
# `systemctl restart nix-daemon`); thereafter this module keeps it consistent.
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.nixConfig;

  mkListLine = key: vs: lib.optional (vs != [ ]) "${key} = ${lib.concatStringsSep " " vs}";

  lines =
    mkListLine "trusted-users" cfg.trustedUsers
    ++ mkListLine "system-features" cfg.systemFeatures
    ++ lib.mapAttrsToList (k: v: "${k} = ${v}") cfg.extraSettings;
in
{
  options.nixfleet.modules.nixConfig = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Manage /etc/nix/nix.custom.conf. On by default for all Ubuntu hosts —
        trusted-users is a prerequisite for nixfleet apply to work at all.
      '';
    };

    trustedUsers = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [
        "root"
        "@wheel"
        "ztaylor"
        "deploy"
      ];
      description = ''
        nix `trusted-users`. `deploy` MUST be present or `nixfleet apply` cannot
        copy locally-built closures to this host. `@wheel` and `ztaylor` keep
        interactive admin access.
      '';
    };

    systemFeatures = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [
        "nixos-test"
        "benchmark"
        "big-parallel"
        "kvm"
      ];
      description = "nix `system-features` (omitted from the file when empty).";
    };

    extraSettings = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = {
        "max-jobs" = "8";
      };
      description = "Additional nix.conf key = value lines to include verbatim.";
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.files."/etc/nix/nix.custom.conf" = {
      mode = "0644";
      owner = "root";
      group = "root";
      text = ''
        # Managed by NixFleet — hosts/<host>.nix modules.nixConfig (do not edit).
        # Determinate Nix's /etc/nix/nix.conf `!include`s this file.
        ${lib.concatStringsSep "\n" lines}
      '';
    };

    nixfleet.systemd.units = {
      "nixfleet-nix-config-apply.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Restart nix-daemon to apply NixFleet-managed nix.custom.conf
          After=nix-daemon.service
          ConditionPathExists=/etc/nix/nix.custom.conf

          [Service]
          Type=oneshot
          ExecStart=/usr/bin/systemctl restart nix-daemon.service
          RemainAfterExit=yes

          [Install]
          WantedBy=multi-user.target
        '';
      };

      "nixfleet-nix-config-apply.path" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Watch /etc/nix/nix.custom.conf for changes

          [Path]
          PathChanged=/etc/nix/nix.custom.conf
          Unit=nixfleet-nix-config-apply.service

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      nixfleet-nix-config-applied = {
        type = "command";
        command = "systemctl show nixfleet-nix-config-apply.service -p Result | grep -q success";
        timeout = 5;
      };
    };
  };
}

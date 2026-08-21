# NixFleet netplan applier
#
# Closes a real gap: modules/dgx-spark-cluster.nix and modules/storage-vlan.nix
# both write /etc/netplan/*.yaml, and nothing in the activation path has ever run
# `netplan apply`. The declared config is therefore correct for the NEXT boot
# while the live link keeps whatever it had — which is how the CX7 fabric sat at
# MTU 1500 for a day after `mtu: 9000` was deployed, costing ~15% of throughput
# with the retransmits buried in NIC counters.
#
# Off by default, and that is deliberate rather than timid. `netplan apply`
# reconfigures live interfaces. Get it wrong on gti or a gtr node and the
# management link goes away — there is no BMC, no AMT and no reachable KVM
# anywhere in this fleet, so recovery is a physical trip. `netplan try`, which
# has a rollback deadman, needs a TTY and can't be driven from a unit. So each
# host opts in once its netplan config has actually been proven.
{
  config,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.netplan;
in
{
  options.nixfleet.modules.netplan = {
    autoApply = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Run `netplan apply` when a NixFleet-managed netplan file changes.

        Enable only on hosts whose netplan config is known-good AND where losing
        the management interface is recoverable. Leaving this off does not make
        the config a lie — the file is on disk and netplan applies it at boot —
        it just means a network change needs a reboot or a hand-run to take
        effect.
      '';
    };
  };

  config = lib.mkIf cfg.autoApply {
    nixfleet.systemd.units = {
      "nixfleet-netplan-apply.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Apply NixFleet-managed netplan configuration
          After=network.target
          ConditionPathIsDirectory=/etc/netplan

          [Service]
          Type=oneshot
          # `netplan generate` first so a malformed file fails here, before
          # apply touches a live interface.
          ExecStartPre=/usr/sbin/netplan generate
          ExecStart=/usr/sbin/netplan apply
          # No RemainAfterExit — see modules/sysctl.nix for why it silently
          # disables the path unit below.

          [Install]
          WantedBy=multi-user.target
        '';
      };

      "nixfleet-netplan-apply.path" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Watch /etc/netplan for NixFleet-managed changes

          [Path]
          PathModified=/etc/netplan
          Unit=nixfleet-netplan-apply.service

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      nixfleet-netplan-applied = {
        type = "command";
        command = "systemctl show nixfleet-netplan-apply.service -p Result | grep -q success";
        timeout = 5;
      };
    };
  };
}

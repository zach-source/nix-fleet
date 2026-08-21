# NixFleet storage VLAN module
#
# Declares the 802.1Q VLAN 8 storage network (192.168.8.0/24, jumbo frames)
# that the fleet uses for iSCSI traffic to the NAS.
#
# This config already exists on gtr-150/151/152 as a hand-written
# /etc/netplan/60-storage-vlan8.yaml and on gti/gtr-153 as a NetworkManager
# profile — five hosts, none of them declared in Nix. This module is the
# declarative version, added when the DGX Sparks needed the same thing.
#
# Two constraints that bite:
#
#   - The VLAN child's MTU cannot exceed its parent's, so the parent physical
#     interface is raised to the same MTU. That parent usually also carries
#     management traffic, so raising it is not a no-op — the switch port must
#     accept jumbo frames on the untagged VLAN too.
#   - The switch port must actually trunk VLAN 8. Nothing here can verify that;
#     an untrunked port brings the interface up with an address and no peers,
#     which looks healthy until something tries to use it. The
#     `storage-vlan-peer` health check exists to catch exactly that.
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.nixfleet.modules.storageVlan;
  vlanIface = "${cfg.interface}.${toString cfg.id}";
in
{
  options.nixfleet.modules.storageVlan = {
    enable = lib.mkEnableOption "802.1Q storage VLAN with jumbo frames";

    interface = lib.mkOption {
      type = lib.types.str;
      description = ''
        Parent physical interface carrying the tagged VLAN. This is normally
        the host's primary uplink — the VLAN rides the same wire as management
        traffic. Its MTU is raised to `mtu`, so confirm the switch port accepts
        jumbo frames before enabling this.
      '';
      example = "enP7s7";
    };

    address = lib.mkOption {
      type = lib.types.str;
      description = ''
        This host's address on the storage network, with prefix. Fleet
        convention mirrors the final octet of the management address:
        192.168.3.140 becomes 192.168.8.140/24.
      '';
      example = "192.168.8.140/24";
    };

    parentDhcp = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Emit dhcp4/dhcp6 on the parent interface as well as its MTU.

        Leave false when another netplan file already declares the parent's
        addressing — netplan merges `ethernets` definitions across files, so on
        the gtr nodes 50-cloud-init.yaml supplies DHCP and this module supplies
        only the MTU.

        Set true when netplan does NOT own the parent. DGX OS is the case that
        forced this option to exist: its netplan is an empty
        `renderer: NetworkManager` stub, and the management address comes from
        an auto-created NetworkManager "Wired connection" profile. Declaring
        the parent there with an MTU alone makes that the *entire* definition,
        NetworkManager replaces its working profile with an address-less one,
        and the host drops off the network — verified the hard way on
        spark-7ee2, which needed a deadman rollback to come back.
      '';
    };

    peer = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Another host on the storage VLAN to ping as a health check. Proves the
        switch port is genuinely trunked rather than just locally configured.
        Null disables that check.
      '';
      example = "192.168.8.133";
    };

    id = lib.mkOption {
      type = lib.types.int;
      default = 8;
      description = "802.1Q VLAN ID.";
    };

    mtu = lib.mkOption {
      type = lib.types.int;
      default = 9000;
      description = ''
        MTU for both the VLAN interface and its parent. 9000 is the fleet
        standard; check `ip -d link show <iface> | grep maxmtu` first, since a
        netplan file asking for more than the NIC supports fails to apply.
      '';
    };

    netplanFile = lib.mkOption {
      type = lib.types.str;
      default = "/etc/netplan/60-storage-vlan8.yaml";
      description = ''
        Path of the generated netplan file. The 60- prefix matches what the gtr
        nodes already carry and orders it after the installer's own config,
        which owns the parent interface's addressing.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # mode 0600 — netplan warns about world-readable configs and refuses some.
    nixfleet.files.${cfg.netplanFile} = {
      mode = "0600";
      owner = "root";
      group = "root";
      # Built line-by-line with explicit indentation rather than as a '' block.
      # Nix strips the common leading whitespace of an indented string, so a
      # conditionally-interpolated chunk lands at column 0 and silently yields
      # YAML whose keys are not nested where they look nested. That is not
      # hypothetical — the dhcp4/dhcp6 lines below hit it, producing a file that
      # parsed as garbage. modules/dgx-spark-cluster.nix carries the same note.
      text = lib.concatStringsSep "\n" (
        [
          "# Managed by NixFleet — modules/storage-vlan.nix (do not edit)."
          "# VLAN ${toString cfg.id} storage network, jumbo frames."
          "network:"
          "  version: 2"
          "  ethernets:"
          "    ${cfg.interface}:"
          "      mtu: ${toString cfg.mtu}"
        ]
        ++ lib.optionals cfg.parentDhcp [
          "      dhcp4: true"
          "      dhcp6: true"
        ]
        ++ [
          "  vlans:"
          "    ${vlanIface}:"
          "      id: ${toString cfg.id}"
          "      link: ${cfg.interface}"
          "      mtu: ${toString cfg.mtu}"
          "      addresses: [${cfg.address}]"
          ""
        ]
      );
      # Only fires where modules.netplan.autoApply is on; elsewhere the file
      # still lands and takes effect at next boot. See modules/netplan.nix.
      restartUnits = [ "nixfleet-netplan-apply.service" ];
    };

    nixfleet.healthChecks = {
      # Catches a netplan file written but never applied — the failure mode that
      # otherwise looks healthy right up until iSCSI tries to use the path.
      storage-vlan-address = {
        type = "command";
        command = "ip -4 addr show ${vlanIface} | grep -q '${cfg.address}'";
      };

      # The MTU is the entire point of this VLAN, so assert it rather than
      # trusting that the parent accepted the raise.
      storage-vlan-mtu = {
        type = "command";
        command = "test \"$(cat /sys/class/net/${vlanIface}/mtu)\" = '${toString cfg.mtu}'";
      };
    }
    // lib.optionalAttrs (cfg.peer != null) {
      # Jumbo-sized ping, unfragmented: proves the switch trunks VLAN 8 AND
      # carries full-size frames. A plain ping passes on a path that silently
      # drops anything over 1500, which is the bug worth catching.
      storage-vlan-peer = {
        type = "command";
        command = "ping -c1 -W2 -M do -s ${toString (cfg.mtu - 28)} ${cfg.peer}";
      };
    };
  };
}

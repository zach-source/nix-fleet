# NixFleet DGX Spark clustering module
#
# Declares the ConnectX-7 fabric that joins two or more DGX Sparks over direct
# 200GbE QSFP links, following NVIDIA's connect-two-sparks playbook.
#
# The thing that trips people up: each physical QSFP port surfaces as TWO Linux
# interfaces, because the NIC reaches the SoC over two independent PCIe Gen5 x4
# links. So one cable gives two "(Up)" lines in `ibdev2netdev`, not one, and
# NVIDIA's playbook puts those two logical interfaces on DIFFERENT /24s
# (192.168.100.0/24 and 192.168.101.0/24) rather than bonding them.
#
#   Left  QSFP -> enp1s0f0np0  + enP2p1s0f0np0   (left = nearest the RJ45 jack,
#   Right QSFP -> enp1s0f1np1  + enP2p1s0f1np1    viewed from the rear)
#
# Two more constraints from the playbook, neither enforceable from here:
#   - cable the SAME physical port on every node, or NCCL tests misbehave
#   - use the same username on every node (discover-sparks assumes `$USER`
#     resolves identically across the fabric)
#
# One cable is enough for full bandwidth. Cable both ports and you must address
# all four interfaces to actually get it.
#
# Ref: https://docs.nvidia.com/dgx/dgx-spark/spark-clustering.html
#      https://github.com/NVIDIA/dgx-spark-playbooks/tree/main/nvidia/connect-two-sparks
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.nixfleet.modules.dgxSparkCluster;

  # NVIDIA's playbook pairs each logical interface with its own subnet. Keeping
  # that mapping here means a host only has to say which node number it is.
  defaultSubnets = {
    enp1s0f1np1 = "192.168.100";
    enP2p1s0f1np1 = "192.168.101";
  };

  # Indentation is written out explicitly rather than relying on a '' block:
  # Nix strips the common leading whitespace of an indented string, so an
  # interpolated multi-line value lands at column 0 and silently produces a
  # netplan file whose interfaces aren't nested under `ethernets:`.
  ifaceLines = lib.concatStringsSep "\n" (
    lib.mapAttrsToList (
      iface: prefix:
      lib.concatStringsSep "\n" [
        "    ${iface}:"
        "      addresses:"
        "        - ${prefix}.${toString cfg.nodeIndex}/24"
        "      dhcp4: no"
        "      mtu: ${toString cfg.mtu}"
      ]
    ) cfg.interfaces
  );
in

{
  options.nixfleet.modules.dgxSparkCluster = {
    enable = lib.mkEnableOption "DGX Spark ConnectX-7 cluster fabric (direct 200GbE QSFP)";

    nodeIndex = lib.mkOption {
      type = lib.types.int;
      description = ''
        Final octet for this node on every cluster subnet. NVIDIA's playbook
        uses 10 for the first Spark and 11 for the second, so a two-node pair is
        nodeIndex 10 and 11. Must be unique across the fabric.
      '';
      example = 10;
    };

    interfaces = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = defaultSubnets;
      description = ''
        Map of ConnectX-7 interface name to its /24 prefix. The default is the
        RIGHT QSFP port's two logical interfaces, matching the playbook's
        example output.

        If you cabled the left port instead, use enp1s0f0np0 and
        enP2p1s0f0np0. Confirm with `ibdev2netdev` — only interfaces reported
        "(Up)" are cabled, and addressing a down interface silently does
        nothing useful.

        Cabling both ports means listing all four here; anything less leaves
        bandwidth on the table.
      '';
    };

    mtu = lib.mkOption {
      type = lib.types.int;
      default = 9000;
      description = ''
        MTU for the fabric interfaces. These are direct-attach cables between
        Sparks with no switch in the path, so jumbo frames cost nothing to
        enable and the default 1500 leaves throughput on the floor.

        Measured spark-5267 -> spark-7ee2 over enp1s0f0np0, iperf3 -t10 -P4:

          mtu 1500 ->  95 Gbit/s, 6678 retransmits
          mtu 9000 -> 110 Gbit/s,    2 retransmits

        It plateaus near 111 Gbit/s at -P8 with zero retransmits, which is a
        TCP/CPU ceiling on 20 cores rather than a link limit — NCCL over RoCE
        bypasses the TCP stack, so treat that number as a floor.
      '';
    };

    netplanFile = lib.mkOption {
      type = lib.types.str;
      default = "/etc/netplan/40-cx7.yaml";
      description = "Path of the generated netplan file. 40- orders it after DGX OS's own configs.";
    };
  };

  config = lib.mkIf cfg.enable {
    # avahi-utils supplies avahi-browse, which NVIDIA's discover-sparks script
    # uses to find peers by mDNS (_ssh._tcp) on the CX7 interfaces. Without it
    # that script exits early with "avahi-browse not found".
    nixfleet.packages = with pkgs; [ avahi ];

    # mode 0600 per the playbook. Netplan warns loudly about world-readable
    # configs and will refuse some of them outright.
    nixfleet.files.${cfg.netplanFile} = {
      mode = "0600";
      owner = "root";
      group = "root";
      text = ''
        # Managed by NixFleet — modules/dgx-spark-cluster.nix (do not edit).
        # ConnectX-7 direct-attach fabric, node index ${toString cfg.nodeIndex}.
        network:
          version: 2
          ethernets:
      ''
      + ifaceLines
      + "\n";
    };

    nixfleet.healthChecks = {
      # Counts cabled ConnectX-7 links. One QSFP cable lights TWO logical
      # interfaces, so the expected count is 2x the number of cables — this is
      # the off-by-half that makes a naive check pass on a half-seated cable.
      dgx-cx7-links-up = {
        type = "command";
        command = "test \"$(ibdev2netdev | grep -c '(Up)')\" -ge ${toString (lib.length (lib.attrNames cfg.interfaces))}";
      };

      # Each configured interface should actually carry its address. Catches a
      # netplan file that was written but never applied (`netplan apply`), which
      # otherwise looks healthy right up until a distributed job fails.
      dgx-cx7-addresses = {
        type = "command";
        command = lib.concatStringsSep " && " (
          lib.mapAttrsToList (
            iface: prefix: "ip -4 addr show ${iface} | grep -q '${prefix}.${toString cfg.nodeIndex}/24'"
          ) cfg.interfaces
        );
      };

      # The addresses check above passes on an interface that carries its IP at
      # the wrong MTU, and NCCL will happily run there — 15% slower, with the
      # retransmits buried in the NIC counters. Assert the MTU separately.
      #
      # This is not hypothetical: writing `mtu: 9000` into the netplan file does
      # NOT apply it. Nothing in the deploy path runs `netplan apply`, so the
      # file is correct for the next boot while the live link stays at 1500.
      dgx-cx7-mtu = {
        type = "command";
        command = lib.concatStringsSep " && " (
          lib.mapAttrsToList (
            iface: _: "test \"$(cat /sys/class/net/${iface}/mtu)\" = '${toString cfg.mtu}'"
          ) cfg.interfaces
        );
      };
    };
  };
}

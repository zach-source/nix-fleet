# DGX Spark 1 (spark-5267) — rank 0 of the stacked pair.
#
# DGX OS is NVIDIA's Ubuntu derivative, so NixFleet manages a Spark exactly like
# an Ubuntu host — packages, files, users, directories, systemd units and health
# checks all behave identically. Note the Spark is aarch64 (GB10 Grace
# Blackwell), so nixpkgs must resolve for aarch64-linux.
#
# The one deliberate exception is OS updates: NixFleet never performs them here.
# That is about ownership, not safety. NVIDIA documents DGX Dashboard as the
# "primary and recommended" update path, and a full manual update is two halves:
#
#   sudo apt update && sudo apt dist-upgrade
#   sudo fwupdmgr refresh && sudo fwupdmgr upgrade && sudo reboot
#
# NixFleet has no firmware story at all, so an apt-only update here would be
# half a job racing the dashboard for the same packages. Leaving the whole lane
# to NVIDIA keeps one owner for updates instead of two.
#
# That exclusion is structural, not a convention to remember:
#   - `nixfleet os-update *` filters on base == "ubuntu", so DGX never enters it
#   - the background update scheduler skips it for the same reason
#   - POST /api/hosts/{name}/apt/upgrade returns 400 on a DGX host
#   - install / remove / autoremove / clean and the read-only apt endpoints all
#     still work, because managing packages is the point
#
# Refs:
#   https://docs.nvidia.com/dgx/dgx-spark/os-and-component-update.html
#   https://docs.nvidia.com/dgx/dgx-spark/dgx-dashboard.html
#   https://docs.nvidia.com/dgx/dgx-spark/spark-clustering.html
{ pkgs, ... }:

{
  imports = [
    ../modules/base-packages.nix
    ../modules/dgx-spark-cluster.nix
    ../modules/netplan.nix
    ../modules/storage-vlan.nix
    ../modules/dspark-dsv4.nix
    # Rank 0 — the head, and the only node that serves HTTP (:8888).
    (import ./dgx-spark-dsv4.nix { nodeRank = 0; })
  ];

  nixfleet = {
    host = {
      name = "dgx-spark-1";
      base = "dgx";
      addr = "192.168.3.140";
    };

    # ConnectX-7 fabric to the other Spark. nodeIndex 10 is NVIDIA's first-node
    # convention; the peer is 11.
    #
    # The LEFT QSFP port is the cabled one here, so the module's right-port
    # default is overridden. Verified on spark-5267 2026-08-20 — ibdev2netdev
    # reports enp1s0f0np0 and enP2p1s0f0np0 (Up) while the right port's pair is
    # (Down). Left and right are electrically identical; NVIDIA's playbook just
    # happens to show the right one, so there is no reason to move the cable.
    modules.dgxSparkCluster = {
      enable = true;
      nodeIndex = 10;
      interfaces = {
        enp1s0f0np0 = "192.168.100";
        enP2p1s0f0np0 = "192.168.101";
      };
    };

    # VLAN 8 storage network, same as the gtr nodes. Parent is the 10GbE RJ45
    # (enP7s7, maxmtu 9194 — verified, so 9000 fits). Peer is gtr-150, which
    # already lives on this VLAN, so the check fails loudly if the switch port
    # isn't trunked for 8.
    # Netplan config on this pair is proven (CX7 fabric + VLAN 8 both verified
    # live), and the management NIC is a separate interface that netplan does
    # not touch here — so applying on change is safe. See modules/netplan.nix.
    modules.netplan.autoApply = true;

    modules.storageVlan = {
      enable = true;
      interface = "enP7s7";
      address = "192.168.8.140/24";
      # DGX OS netplan is an empty `renderer: NetworkManager` stub — nothing
      # else declares this interface, so the module must carry its addressing
      # or NetworkManager replaces a working profile with an address-less one.
      parentDhcp = true;
      peer = "192.168.8.133";
    };

    # Ordinary package management — the part that works like any other host.
    # Deliberately nothing NVIDIA here: the driver, CUDA and firmware arrive
    # with DGX OS and stay NVIDIA's to version.
    #
    # No apt.hold entries either. NVIDIA's update guide names no metapackages
    # and does not ask you to pin anything, so inventing holds would just fight
    # the dashboard. NixFleet already refuses to upgrade this host.
    # Declared because it does not exist on a stock DGX OS image (checked on
    # spark-5267). Activation creates groups before directories, so the chown
    # below resolves.
    groups.dgxusers = { };

    directories = {
      "/srv/datasets" = {
        mode = "0775";
        owner = "root";
        group = "dgxusers";
      };
    };

    healthChecks = {
      # Confirms the GPU is enumerable. Note `nvidia-smi` on Spark reports
      # "Memory-Usage: Not Supported" — that is a documented known issue on
      # this platform, not a fault, so don't health-check memory here.
      gpu-present = {
        type = "command";
        command = "nvidia-smi -L | grep -q GPU";
      };

      # DGX Dashboard is the built-in web UI on port 11000 and owns updates on
      # this box. It binds loopback — reach it with
      #   ssh -L 11000:localhost:11000 <user>@<host>
      # so this check stays local rather than opening a firewall hole.
      dgx-dashboard = {
        type = "http";
        url = "http://localhost:11000";
      };

      # ConnectX-7 link check for a stacked pair. Each QSFP port surfaces as
      # TWO Linux interfaces (the NIC reaches the SoC over two PCIe Gen5 x4
      # links), so one cable gives two "Up" lines and two cables give four.
      #
      #   Left  port -> enp1s0f0np0  / enP2p1s0f0np0
      #   Right port -> enp1s0f1np1  / enP2p1s0f1np1
      #
      # "Left" is the port nearest the Ethernet jack, viewed from the rear;
      # the connect-two-sparks playbook assumes a specific port, so match it.
      # Expect 2 for a single cable — raise to 4 if you cable both ports.
      connectx-link-up = {
        type = "command";
        command = "test \"$(ibdev2netdev | grep -c '(Up)')\" -ge 2";
      };
    };
  };
}

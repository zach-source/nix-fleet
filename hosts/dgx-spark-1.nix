# Example DGX Spark host configuration
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
    ../modules/vllm.nix
    # Rank 0 — the master, and the only node that serves HTTP (:8000).
    (import ./dgx-spark-dsv4.nix { nodeRank = 0; })
  ];

  nixfleet = {
    host = {
      name = "dgx-spark-1";
      base = "dgx";
      addr = "10.0.3.10";
    };

    # ConnectX-7 fabric to the other Spark. nodeIndex 10 is NVIDIA's first-node
    # convention; the peer would be 11. Defaults to the RIGHT QSFP port's two
    # logical interfaces — run `ibdev2netdev` and match whichever port you
    # actually cabled, using the same physical port on both machines.
    modules.dgxSparkCluster = {
      enable = true;
      nodeIndex = 10;
    };

    # Ordinary package management — the part that works like any other host.
    # Deliberately nothing NVIDIA here: the driver, CUDA and firmware arrive
    # with DGX OS and stay NVIDIA's to version.
    #
    # No apt.hold entries either. NVIDIA's update guide names no metapackages
    # and does not ask you to pin anything, so inventing holds would just fight
    # the dashboard. NixFleet already refuses to upgrade this host.
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

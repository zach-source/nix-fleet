# NixFleet hyperscale node module
#
# Host prerequisites for a KubeVirt + LINSTOR/DRBD (Piraeus) storage worker,
# the HPE DL380 Gen9 "hs" hosts. Piraeus runs LINSTOR in the cluster; the host
# only has to supply LVM thin provisioning, the device-mapper modules, and
# kernel headers (Piraeus' drbd-module-loader builds DRBD against the running
# kernel).
#
# What it does:
#   - installs the distro lvm2, thin-provisioning-tools and
#     linux-headers-<running kernel> (apt) if missing
#   - persists the dm_thin_pool and dm_snapshot modules (modules-load.d)
#   - labels the k8s node piraeus.io/satellite=true (runs a LINSTOR
#     satellite), piraeus.io/storage=true (gets a storage pool) and
#     nixfleet.io/hyperscale=true (kubelet --node-labels, via k0s.nix
#     extraKubeletArgs)
#   - with dataDisks set, nixfleet-hyperscale-vg.service creates VG `linstor`
#     and thin pool `thin` (100%FREE) on those disks, once, if the VG does not
#     exist yet. This is the LINSTOR storage pool: LVM_THIN, linstor/thin.
#
# The VG service NEVER wipes anything. A listed disk that carries any
# filesystem, RAID, LVM or partition signature (wipefs -n / blkid) makes the
# service fail before any disk is touched; clear such a disk by hand first.
# Adding disks later does not extend an existing VG.
#
# Usage:
#   imports = [ ../modules/hyperscale.nix ../modules/k0s.nix ];
#   nixfleet.modules.hyperscale = {
#     enable = true;
#     dataDisks = [ "/dev/disk/by-id/scsi-3600..." ];
#   };
{
  config,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.hyperscale;
  disks = lib.escapeShellArgs (map toString cfg.dataDisks);
in
{
  options.nixfleet.modules.hyperscale = {
    enable = lib.mkEnableOption "hyperscale node prerequisites (LINSTOR/DRBD thin-LVM storage)";

    dataDisks = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "/dev/disk/by-id/scsi-3600508b1001c0000000000000000000" ];
      description = ''
        Whole disks (/dev/disk/by-id paths) to pool into VG `linstor` with
        thin pool `thin`. Empty creates nothing. Disks with any existing
        signature are refused, never wiped.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.files = {
      "/etc/modules-load.d/hyperscale.conf" = {
        mode = "0644";
        owner = "root";
        group = "root";
        text = ''
          # Managed by NixFleet hyperscale module — LVM thin pool + snapshots
          dm_thin_pool
          dm_snapshot
        '';
      };
    }
    // lib.optionalAttrs (cfg.dataDisks != [ ]) {
      "/usr/local/bin/nixfleet-hyperscale-vg" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          # Already provisioned: nothing to do, and never touch the disks again.
          if vgs linstor >/dev/null 2>&1; then
            echo "hyperscale: VG linstor exists, skipping"
            exit 0
          fi

          # Check every disk before changing any of them.
          for d in "$@"; do
            [ -b "$d" ] || { echo "hyperscale: $d is not a block device" >&2; exit 1; }
            if [ -n "$(wipefs -n "$d")" ] || blkid -p "$d" >/dev/null 2>&1; then
              echo "hyperscale: $d has an existing signature, refusing (clear it by hand):" >&2
              wipefs -n "$d" >&2 || true
              exit 1
            fi
          done

          pvcreate "$@"
          vgcreate linstor "$@"
          lvcreate -l 100%FREE --thinpool thin linstor
          echo "hyperscale: created VG linstor with thin pool thin on $*"
        '';
      };
    };

    nixfleet.systemd.units = lib.mkIf (cfg.dataDisks != [ ]) {
      "nixfleet-hyperscale-vg.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Create LINSTOR thin pool (VG linstor) on hyperscale data disks
          After=local-fs.target systemd-udev-settle.service
          # LINSTOR/Piraeus expects the pool before the kubelet schedules satellites.
          Before=k0sworker.service

          [Service]
          Type=oneshot
          RemainAfterExit=yes
          ExecStart=/usr/local/bin/nixfleet-hyperscale-vg ${disks}

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.k0s.worker.extraKubeletArgs = [
      "--node-labels=piraeus.io/satellite=true,piraeus.io/storage=true,nixfleet.io/hyperscale=true"
    ];

    nixfleet.hooks.postActivate = ''
      # --- hyperscale prerequisites (NixFleet hyperscale module) ---
      # Distro packages, so lvm2 integrates with the host udev/systemd and the
      # headers match the running kernel. Match "ii" to require a full install.
      for pkg in lvm2 thin-provisioning-tools "linux-headers-$(uname -r)"; do
        if ! dpkg -l "$pkg" 2>/dev/null | grep -q '^ii'; then
          echo "hyperscale: installing $pkg"
          DEBIAN_FRONTEND=noninteractive apt-get install -y "$pkg" \
            || echo "hyperscale: WARNING apt-get install $pkg failed"
        fi
      done

      modprobe dm_thin_pool 2>/dev/null || echo "hyperscale: WARNING modprobe dm_thin_pool failed"
      modprobe dm_snapshot 2>/dev/null || echo "hyperscale: WARNING modprobe dm_snapshot failed"
    '';
  };
}

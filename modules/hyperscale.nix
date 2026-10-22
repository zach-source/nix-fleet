# NixFleet hyperscale node module
#
# Host prerequisites for a KubeVirt + LINSTOR/DRBD (Piraeus) storage worker,
# the HPE DL380 Gen9 "hs" hosts. Piraeus runs LINSTOR in the cluster. The host
# supplies ZFS, two zpools and kernel headers. Piraeus' drbd-module-loader
# builds DRBD against the running kernel.
#
# Disk layout:
#   - zpool `linstor`: mirror vdevs of the data disks, no mountpoint.
#     LINSTOR makes zvols in it. The LINSTOR storage pool is ZFS_THIN on
#     zpool `linstor`.
#   - zpool `scratch`: one mirror, mounted at /var/lib/k0s. It holds
#     container images, emptyDir data and kubelet data. Dataset scratch/ate
#     is mounted at /var/lib/ate, the image and snapshot cache of the
#     Agent Substrate atelet.
#
# What it does:
#   - installs the distro zfsutils-linux and linux-headers-<running kernel>
#     (apt) if missing. This runs in preActivate, because the pool service
#     starts before postActivate and needs zpool.
#   - loads the zfs module now and at boot (modules-load.d)
#   - caps the ZFS ARC at arcMaxGiB (modprobe.d for boot, sysfs for now), so
#     the kubelet and the VMs keep their memory
#   - labels the k8s node piraeus.io/satellite=true (runs a LINSTOR
#     satellite), piraeus.io/storage=true (gets a storage pool) and
#     nixfleet.io/hyperscale=true (kubelet --node-labels, via k0s.nix
#     extraKubeletArgs)
#   - nixfleet-hyperscale-zpool.service creates each configured pool once.
#     It imports a pool that exists on the disks but not on the host. It
#     skips a pool that is already imported.
#
# The pool service NEVER wipes anything. A listed disk that carries any
# filesystem, RAID, LVM, ZFS or partition signature (wipefs -n / blkid) makes
# the service fail before it touches any disk. Clear such a disk by hand
# first. Adding disks later does not extend an existing pool.
#
# Usage:
#   imports = [ ../modules/hyperscale.nix ../modules/k0s.nix ];
#   nixfleet.modules.hyperscale = {
#     enable = true;
#     linstorMirrors = [ [ "/dev/disk/by-id/wwn-0xA" "/dev/disk/by-id/wwn-0xB" ] ];
#     scratchMirror = [ "/dev/disk/by-id/wwn-0xC" "/dev/disk/by-id/wwn-0xD" ];
#   };
{
  config,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.hyperscale;
  arcBytes = toString (cfg.arcMaxGiB * 1024 * 1024 * 1024);

  hasLinstor = cfg.linstorMirrors != [ ];
  hasScratch = cfg.scratchMirror != [ ];
  hasPools = hasLinstor || hasScratch;

  script = "/usr/local/bin/nixfleet-hyperscale-zpool";
  linstorSpec = lib.escapeShellArgs (lib.concatMap (m: [ "mirror" ] ++ m) cfg.linstorMirrors);
  scratchSpec = lib.escapeShellArgs ([ "mirror" ] ++ cfg.scratchMirror);

  execStarts = lib.concatStringsSep "\n" (
    lib.optional hasLinstor "ExecStart=${script} linstor none ${linstorSpec}"
    ++ lib.optionals hasScratch [
      "ExecStart=${script} scratch /var/lib/k0s ${scratchSpec}"
      "ExecStart=/bin/sh -c 'zfs list -H scratch/ate >/dev/null 2>&1 || zfs create -o mountpoint=/var/lib/ate scratch/ate'"
    ]
  );
in
{
  options.nixfleet.modules.hyperscale = {
    enable = lib.mkEnableOption "hyperscale node prerequisites (LINSTOR/DRBD on ZFS storage)";

    linstorMirrors = lib.mkOption {
      type = lib.types.listOf (lib.types.listOf lib.types.str);
      default = [ ];
      example = [
        [
          "/dev/disk/by-id/wwn-0xA"
          "/dev/disk/by-id/wwn-0xB"
        ]
      ];
      description = ''
        Mirror vdevs for zpool `linstor`. Each inner list is one mirror of
        /dev/disk/by-id paths. The pool has no mountpoint (-m none), because
        LINSTOR makes zvols in it. Empty creates nothing. Disks with any
        existing signature are refused, never wiped.
      '';
    };

    scratchMirror = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [
        "/dev/disk/by-id/wwn-0xC"
        "/dev/disk/by-id/wwn-0xD"
      ];
      description = ''
        Two /dev/disk/by-id paths for one mirror in zpool `scratch`, mounted
        at /var/lib/k0s (container images, emptyDir and kubelet data). Empty
        creates nothing. Disks with any existing signature are refused,
        never wiped.
      '';
    };

    arcMaxGiB = lib.mkOption {
      type = lib.types.int;
      default = 32;
      description = ''
        Upper limit of the ZFS ARC in GiB. The cap keeps memory free for the
        kubelet and the VMs.
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
          # Managed by NixFleet hyperscale module — ZFS
          zfs
        '';
      };

      "/etc/modprobe.d/zfs-arc.conf" = {
        mode = "0644";
        owner = "root";
        group = "root";
        text = ''
          # Managed by NixFleet hyperscale module — ZFS ARC cap (${toString cfg.arcMaxGiB} GiB)
          options zfs zfs_arc_max=${arcBytes}
        '';
      };
    }
    # /var/lib/k0s must be the scratch pool before the kubelet or the kubevirt
    # bind writes to it. Before= in the pool unit orders a boot only. These
    # drop-ins also order a first activation.
    // lib.optionalAttrs hasScratch (
      lib.genAttrs
        [
          "/etc/systemd/system/k0sworker.service.d/10-hyperscale-zpool.conf"
          "/etc/systemd/system/nixfleet-kubevirt-kubelet-bind.service.d/10-hyperscale-zpool.conf"
        ]
        (_: {
          mode = "0644";
          owner = "root";
          group = "root";
          text = ''
            # Managed by NixFleet hyperscale module
            [Unit]
            Requires=nixfleet-hyperscale-zpool.service
            After=nixfleet-hyperscale-zpool.service
          '';
        })
    )
    // lib.optionalAttrs hasPools {
      "${script}" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          # Usage: nixfleet-hyperscale-zpool <pool> <mountpoint|none> <vdev spec...>
          # Example spec: mirror /dev/disk/by-id/a /dev/disk/by-id/b mirror ...
          set -euo pipefail

          pool="$1"
          mountpoint="$2"
          shift 2

          # Already imported: nothing to do, and never touch the disks again.
          if zpool list -H "$pool" >/dev/null 2>&1; then
            echo "hyperscale: zpool $pool exists, skipping"
            exit 0
          fi

          # The pool exists on the disks but the cache file is gone.
          if zpool import -N "$pool" >/dev/null 2>&1; then
            zfs mount -a
            echo "hyperscale: imported existing zpool $pool"
            exit 0
          fi

          # Check every disk before changing any of them.
          for d in "$@"; do
            [ "$d" = mirror ] && continue
            [ -b "$d" ] || { echo "hyperscale: $d is not a block device" >&2; exit 1; }
            if [ -n "$(wipefs -n "$d")" ] || blkid -p "$d" >/dev/null 2>&1; then
              echo "hyperscale: $d has an existing signature, refusing (clear it by hand):" >&2
              wipefs -n "$d" >&2 || true
              exit 1
            fi
          done

          zpool create -o ashift=12 -O compression=lz4 -O atime=off -O xattr=sa \
            -O acltype=posixacl -m "$mountpoint" "$pool" "$@"
          echo "hyperscale: created zpool $pool on $*"
        '';
      };
    };

    nixfleet.systemd.units = lib.mkIf hasPools {
      "nixfleet-hyperscale-zpool.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Create ZFS pools (linstor, scratch) on hyperscale data disks
          After=local-fs.target systemd-udev-settle.service zfs-import.target
          # The pools must exist before the kubelet starts.
          Before=k0sworker.service

          [Service]
          Type=oneshot
          RemainAfterExit=yes
          ${execStarts}

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.k0s.worker.extraKubeletArgs = [
      "--node-labels=piraeus.io/satellite=true,piraeus.io/storage=true,nixfleet.io/hyperscale=true"
    ];

    nixfleet.hooks.preActivate = ''
      # --- hyperscale prerequisites (NixFleet hyperscale module) ---
      # Distro packages, so ZFS integrates with the host udev/systemd and the
      # headers match the running kernel. Match "ii" to require a full install.
      for pkg in zfsutils-linux "linux-headers-$(uname -r)"; do
        if ! dpkg -l "$pkg" 2>/dev/null | grep -q '^ii'; then
          echo "hyperscale: installing $pkg"
          DEBIAN_FRONTEND=noninteractive apt-get install -y "$pkg" \
            || echo "hyperscale: WARNING apt-get install $pkg failed"
        fi
      done

      modprobe zfs 2>/dev/null || echo "hyperscale: WARNING modprobe zfs failed"
      echo ${arcBytes} > /sys/module/zfs/parameters/zfs_arc_max 2>/dev/null \
        || echo "hyperscale: WARNING could not set zfs_arc_max"
    '';
  };
}

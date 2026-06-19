# NixFleet Backup Module
# Mounts the Synology NFS backup share and ships ZFS snapshots to it.
{
  config,
  pkgs,
  lib,
  ...
}:
let
  # Synology NFS backup target. The share is on the 192.168.1.x management
  # subnet (the gtr/gti hosts route to it). NOTE: the export is currently `*`
  # (allow-all) on the NAS — tighten it to the host source IPs on the Synology.
  nfsServer = "192.168.1.67";
  nfsExport = "/volume1/k0s-gti";
in
{
  # ============================================================================
  # Packages required for backup operations
  # ============================================================================
  nixfleet.packages = with pkgs; [
    nfs-utils # NFS mounting (mount.nfs / showmount)
    pv # Progress viewer for zfs send
    mbuffer # Buffer for network transfers
  ];

  # ============================================================================
  # Mount directories
  # ============================================================================
  nixfleet.directories = {
    # NFS mount point for backups
    "/mnt/backup" = {
      mode = "0755";
      owner = "root";
      group = "root";
    };
    # Local backup staging area
    "/var/lib/nixfleet/backups" = {
      mode = "0750";
      owner = "root";
      group = "root";
    };
  };

  nixfleet.files = {
    # ============================================================================
    # ZFS Snapshot Backup Script
    # ============================================================================
    "/usr/local/bin/zfs-backup" = {
      mode = "0755";
      owner = "root";
      group = "root";
      text = ''
        #!/bin/bash
        set -euo pipefail

        # ZFS Snapshot Backup Script for NixFleet
        # Backs up ZFS snapshots to NFS mount

        BACKUP_MOUNT="/mnt/backup"
        HOSTNAME=$(hostname -s)
        BACKUP_DIR="$BACKUP_MOUNT/$HOSTNAME"
        LOG_FILE="/var/log/zfs-backup.log"
        RETENTION_DAYS=30

        # Log to stderr (and the file), NOT stdout: create_snapshot's result is
        # captured via $(...), so any log output on stdout would be slurped into
        # the snapshot name ("invalid character '[' in name").
        log() {
          echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOG_FILE" >&2
        }

        create_snapshot() {
          local pool=$1
          local snap_name="nixfleet-$(date +%Y%m%d-%H%M%S)"
          log "Creating snapshot $pool@$snap_name"
          zfs snapshot -r "$pool@$snap_name"
          echo "$snap_name"
        }

        backup_snapshot() {
          local pool=$1
          local snap_name=$2
          local dest_file="$BACKUP_DIR/$pool-$snap_name.zfs"

          mkdir -p "$BACKUP_DIR"

          # Find previous snapshot for incremental
          local prev_snap=$(zfs list -t snapshot -o name -s creation "$pool" 2>/dev/null | grep "nixfleet-" | tail -2 | head -1 | cut -d@ -f2)

          if [ -n "$prev_snap" ] && [ "$prev_snap" != "$snap_name" ]; then
            log "Incremental backup: $pool@$prev_snap -> $pool@$snap_name"
            zfs send -i "$pool@$prev_snap" "$pool@$snap_name" | pv -f 2>>$LOG_FILE | gzip > "$dest_file.inc.gz"
          else
            log "Full backup: $pool@$snap_name"
            zfs send "$pool@$snap_name" | pv -f 2>>$LOG_FILE | gzip > "$dest_file.full.gz"
          fi
        }

        cleanup_old_snapshots() {
          local pool=$1
          log "Cleaning up snapshots older than $RETENTION_DAYS days for $pool"

          # IMPORTANT: -r is required. Snapshots are created with `zfs snapshot -r`
          # (recursive over every child dataset), so without -r here we only ever
          # see/destroy the top-level pool snapshot and the child-dataset
          # snapshots (e.g. bpool/BOOT/ubuntu_*@nixfleet-...) accumulate forever
          # until the pool fills — which is exactly how a 1.88 GiB bpool reached
          # 92% with 178 un-pruned /boot snapshots. Listing recursively and
          # destroying each enumerated snapshot also cleans up any pre-existing
          # orphans left by the previous non-recursive logic.
          zfs list -t snapshot -r -o name,creation -p "$pool" 2>/dev/null | \
            grep "nixfleet-" | while read -r snap creation; do
              age_days=$(( ($(date +%s) - creation) / 86400 ))
              if [ "$age_days" -gt "$RETENTION_DAYS" ]; then
                log "Removing old snapshot: $snap"
                zfs destroy "$snap" || true
              fi
            done
        }

        cleanup_old_backups() {
          log "Cleaning up backup files older than $RETENTION_DAYS days"
          find "$BACKUP_DIR" -name "*.zfs.*.gz" -mtime +$RETENTION_DAYS -delete 2>/dev/null || true
        }

        main() {
          log "=== ZFS Backup Started ==="

          # Local snapshotting + retention must NOT depend on the remote backup
          # mount being available. Previously main() ran check_mount first and
          # exited, so when the backup NAS was unreachable nothing ran — but the
          # bigger hazard is the opposite: if snapshots are created without
          # cleanup ever running, the pool fills. So we always create + prune
          # snapshots locally, and only perform the off-host send when the mount
          # is present.
          local have_mount=false
          if mountpoint -q "$BACKUP_MOUNT"; then
            have_mount=true
          else
            log "WARN: backup mount $BACKUP_MOUNT not available — snapshotting locally, skipping off-host send"
          fi

          # Get all ZFS pools
          for pool in $(zpool list -Ho name); do
            log "Processing pool: $pool"

            # Create snapshot (recursive)
            snap_name=$(create_snapshot "$pool")

            # Send off-host only when the backup mount is present
            if [ "$have_mount" = true ]; then
              backup_snapshot "$pool" "$snap_name"
            fi

            # Always prune old snapshots so the pool can't fill
            cleanup_old_snapshots "$pool"
          done

          # Cleanup old backup files (only meaningful when the mount is present)
          if [ "$have_mount" = true ]; then
            cleanup_old_backups
          fi

          log "=== ZFS Backup Completed ==="
        }

        main "$@"
      '';
    };
  };

  # ============================================================================
  # Systemd units for automatic mounting and backup
  # ============================================================================
  nixfleet.systemd.units = {
    # NFS backup mount (Synology)
    "mnt-backup.mount" = {
      enabled = true;
      text = ''
        [Unit]
        Description=NFS mount for backups (Synology ${nfsServer})
        After=network-online.target
        Wants=network-online.target

        [Mount]
        What=${nfsServer}:${nfsExport}
        Where=/mnt/backup
        Type=nfs
        # nofail/_netdev: don't block boot if the NAS is unreachable. hard: retry
        # rather than error out on transient NAS hiccups during a long zfs send.
        Options=rw,hard,nofail,_netdev,noatime

        [Install]
        WantedBy=multi-user.target
      '';
    };

    # ZFS backup timer
    "zfs-backup.timer" = {
      enabled = true;
      text = ''
        [Unit]
        Description=Daily ZFS snapshot backup

        [Timer]
        OnCalendar=*-*-* 02:00:00
        Persistent=true
        RandomizedDelaySec=1800

        [Install]
        WantedBy=timers.target
      '';
    };

    # ZFS backup service
    "zfs-backup.service" = {
      enabled = true;
      text = ''
        [Unit]
        Description=ZFS snapshot backup to NFS
        # Wants, not Requires: the script snapshots + prunes locally regardless of
        # the backup mount (only the off-host send needs it). A hard Requires meant
        # that when the backup NAS was unreachable the service never ran at all, so
        # nothing pruned and the pools filled. After= keeps ordering when present.
        After=mnt-backup.mount
        Wants=mnt-backup.mount

        [Service]
        Type=oneshot
        ExecStart=/usr/local/bin/zfs-backup
        Nice=19
        IOSchedulingClass=idle
      '';
    };
  };

  # ============================================================================
  # Health checks
  # ============================================================================
  nixfleet.healthChecks = {
    nfs-backup-mount = {
      type = "command";
      command = "mountpoint -q /mnt/backup";
      timeout = 5;
    };
    backup-recent = {
      type = "command";
      command = "find /mnt/backup/$(hostname -s) -name '*.zfs.*.gz' -mtime -2 | grep -q .";
      timeout = 10;
    };
  };
}

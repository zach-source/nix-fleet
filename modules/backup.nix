# NixFleet Backup Module
# Configures SMB personal drive mounts, NFS backup mounts, and ZFS snapshot backups
{
  config,
  pkgs,
  lib,
  ...
}:
{
  # ============================================================================
  # Packages required for backup operations
  # ============================================================================
  nixfleet.packages = with pkgs; [
    cifs-utils # SMB/CIFS mounting
    # nfs-utils # NFS mounting (not needed, using SMB)
    pv # Progress viewer for zfs send
    mbuffer # Buffer for network transfers
  ];

  # ============================================================================
  # Mount directories
  # ============================================================================
  nixfleet.directories = {
    # SMB mount point for personal drives
    "/mnt/personal" = {
      mode = "0755";
      owner = "root";
      group = "root";
    };
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

  # ============================================================================
  # SMB credentials file (decrypted from age secret)
  # ============================================================================
  nixfleet.files = {
    "/etc/nixfleet/smb-credentials" = {
      mode = "0600";
      owner = "root";
      group = "root";
      # This will be populated by secrets deployment
      text = "# Placeholder - deploy with: nixfleet secrets deploy";
    };

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

        log() {
          echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOG_FILE"
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

    # ============================================================================
    # Mount Script (handles both SMB and NFS)
    # ============================================================================
    "/usr/local/bin/nixfleet-mount" = {
      mode = "0755";
      owner = "root";
      group = "root";
      text = ''
        #!/bin/bash
        set -euo pipefail

        # Mount SMB personal drive
        mount_smb() {
          local mount_point="/mnt/personal"
          local smb_server="192.168.3.95"
          local smb_share="Personal-Drive"
          local creds_file="/etc/nixfleet/smb-credentials"

          if mountpoint -q "$mount_point"; then
            echo "SMB already mounted at $mount_point"
            return 0
          fi

          if [ ! -f "$creds_file" ]; then
            echo "ERROR: SMB credentials not found at $creds_file"
            echo "Deploy secrets with: nixfleet secrets deploy"
            return 1
          fi

          echo "Mounting SMB share //$smb_server/$smb_share to $mount_point"
          mount -t cifs "//$smb_server/$smb_share" "$mount_point" \
            -o credentials="$creds_file",uid=1000,gid=1000,file_mode=0644,dir_mode=0755
        }

        # Mount SMB backup drive
        mount_backup() {
          local mount_point="/mnt/backup"
          local smb_server="192.168.3.95"
          local smb_share="NFS_Drive"
          local creds_file="/etc/nixfleet/smb-credentials"

          if mountpoint -q "$mount_point"; then
            echo "Backup already mounted at $mount_point"
            return 0
          fi

          if [ ! -f "$creds_file" ]; then
            echo "ERROR: SMB credentials not found at $creds_file"
            return 1
          fi

          echo "Mounting SMB share //$smb_server/$smb_share to $mount_point"
          mount -t cifs "//$smb_server/$smb_share" "$mount_point" \
            -o credentials="$creds_file",uid=1000,gid=1000,file_mode=0644,dir_mode=0755
        }

        # Unmount all
        unmount_all() {
          echo "Unmounting all NixFleet mounts..."
          umount /mnt/personal 2>/dev/null || true
          umount /mnt/backup 2>/dev/null || true
        }

        case "''${1:-all}" in
          personal)
            mount_smb
            ;;
          backup)
            mount_backup
            ;;
          all)
            mount_smb
            mount_backup
            ;;
          unmount|umount)
            unmount_all
            ;;
          status)
            echo "=== Mount Status ==="
            mountpoint -q /mnt/personal && echo "Personal (SMB): mounted" || echo "Personal (SMB): not mounted"
            mountpoint -q /mnt/backup && echo "Backup (SMB): mounted" || echo "Backup (SMB): not mounted"
            ;;
          *)
            echo "Usage: $0 {personal|backup|all|unmount|status}"
            exit 1
            ;;
        esac
      '';
    };
  };

  # ============================================================================
  # Systemd units for automatic mounting and backup
  # ============================================================================
  nixfleet.systemd.units = {
    # SMB mount service
    "mnt-personal.mount" = {
      enabled = true;
      text = ''
        [Unit]
        Description=SMB mount for personal drive
        After=network-online.target
        Wants=network-online.target

        [Mount]
        What=//192.168.3.95/Personal-Drive
        Where=/mnt/personal
        Type=cifs
        Options=credentials=/etc/nixfleet/smb-credentials,uid=1000,gid=1000,file_mode=0644,dir_mode=0755,_netdev,nofail

        [Install]
        WantedBy=multi-user.target
      '';
    };

    # SMB backup mount service
    "mnt-backup.mount" = {
      enabled = true;
      text = ''
        [Unit]
        Description=SMB mount for backups
        After=network-online.target
        Wants=network-online.target

        [Mount]
        What=//192.168.3.95/NFS_Drive
        Where=/mnt/backup
        Type=cifs
        Options=credentials=/etc/nixfleet/smb-credentials,uid=1000,gid=1000,file_mode=0644,dir_mode=0755,_netdev,nofail

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
        After=mnt-backup.mount
        Requires=mnt-backup.mount

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
    smb-mount = {
      type = "command";
      command = "mountpoint -q /mnt/personal";
      timeout = 5;
    };
    nfs-mount = {
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

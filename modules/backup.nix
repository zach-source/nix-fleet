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
        # Backs up ZFS snapshots to NFS mount.
        #
        # Layout on the backup target:
        #
        #   $BACKUP_DIR/<pool>/<full-snapshot>/full.zfs        base of the chain
        #   $BACKUP_DIR/<pool>/<full-snapshot>/inc-<snapshot>.zfs
        #   $BACKUP_DIR/<pool>/<full-snapshot>/tip             snapshot the target holds
        #
        # A chain is self-contained: a restore needs full.zfs plus every inc-*
        # in the SAME directory, applied in name order:
        #
        #   zfs receive -Fdu <pool> < full.zfs
        #   for f in inc-*.zfs; do zfs receive -Fdu <pool> < "$f"; done

        BACKUP_MOUNT="/mnt/backup"
        HOSTNAME=$(hostname -s)
        BACKUP_DIR="$BACKUP_MOUNT/$HOSTNAME"
        LOG_FILE="/var/log/zfs-backup.log"
        RETENTION_DAYS=30      # local snapshot retention
        FULL_INTERVAL_DAYS=30  # start a new chain (fresh full send) at least this often
        KEEP_CHAINS=2          # complete chains kept on the backup target

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

        # zfs send flags, all three load-bearing:
        #
        #   -R  Recursive replication stream. WITHOUT IT only the top-level pool
        #       dataset is sent, which on a ZFS-root host holds essentially
        #       nothing — that is the bug that shipped ~165-byte "backups" every
        #       night for months while the service exited 0.
        #   -w  Raw. rpool is encrypted (aes-256-gcm) and a plain -R refuses with
        #       "encrypted dataset may not be sent with properties without the
        #       raw flag". Raw also means the NAS only ever stores ciphertext.
        #
        # No gzip: a raw stream of an encrypted dataset is incompressible, so
        # compressing it burned CPU for nothing.
        send_stream() {
          local dest=$1
          shift
          local tmp="$dest.partial"
          log "  -> $dest"
          # pipefail is set, so a failed zfs send fails the pipeline. Write to
          # .partial and rename only on success: a truncated stream must never
          # be mistaken for a usable backup.
          if zfs send "$@" | pv -f 2>>"$LOG_FILE" > "$tmp"; then
            mv "$tmp" "$dest"
            return 0
          fi
          rm -f "$tmp"
          return 1
        }

        chain_dirs() {
          local pool=$1
          find "$BACKUP_DIR/$pool" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort
        }

        # A chain only counts once its base full.zfs has landed. Anything else is
        # debris from an interrupted send: drop it so it neither occupies a
        # retention slot nor leaves a half-written stream on the NAS forever.
        sweep_partial_chains() {
          local pool=$1
          local d
          chain_dirs "$pool" | while read -r d; do
            if [ ! -f "$d/full.zfs" ]; then
              log "$pool: removing incomplete chain $(basename "$d")"
              rm -rf "$d"
            fi
          done
        }

        backup_pool() {
          local pool=$1
          local snap=$2
          local pool_dir="$BACKUP_DIR/$pool"
          local chain=""
          local tip=""
          local full_age=0

          mkdir -p "$pool_dir"
          sweep_partial_chains "$pool"
          chain=$(chain_dirs "$pool" | tail -1)

          if [ -n "$chain" ] && [ -f "$chain/tip" ]; then
            tip=$(cat "$chain/tip")
            full_age=$(( ( $(date +%s) - $(stat -c %Y "$chain/full.zfs") ) / 86400 ))
          fi

          # Fall back to a full send when there is no usable chain, the chain is
          # due for renewal, or the snapshot the target ends at has been pruned
          # locally — an incremental can only be built from a source that both
          # sides still have. The old script picked the source from the local
          # snapshot list instead, so any day the NAS was unreachable (snapshot
          # taken, send skipped) silently produced an increment nothing could
          # apply.
          if [ -z "$tip" ]; then
            log "$pool: no usable chain on target — full send"
          elif [ "$full_age" -ge "$FULL_INTERVAL_DAYS" ]; then
            log "$pool: chain base is $full_age days old — starting a new chain"
            tip=""
          elif ! zfs list -t snapshot -H -o name "$pool@$tip" >/dev/null 2>&1; then
            log "$pool: target tip $pool@$tip is gone locally — full send"
            tip=""
          fi

          if [ -n "$tip" ]; then
            log "$pool: incremental $tip -> $snap"
            send_stream "$chain/inc-$snap.zfs" -Rw -i "$pool@$tip" "$pool@$snap" || return 1
          else
            chain="$pool_dir/$snap"
            mkdir -p "$chain"
            log "$pool: full send $snap (new chain)"
            send_stream "$chain/full.zfs" -Rw "$pool@$snap" || return 1
          fi

          echo "$snap" > "$chain/tip"
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

        # Retention is per CHAIN, never per file. The previous `find -mtime +30
        # -delete` aged out individual streams, so once the base full of a chain
        # passed 30 days every later increment in it became unrestorable while
        # still sitting on the NAS looking like a backup.
        cleanup_old_chains() {
          local pool=$1
          local total drop d
          total=$(chain_dirs "$pool" | wc -l)
          drop=$(( total - KEEP_CHAINS ))
          [ "$drop" -gt 0 ] || return 0
          chain_dirs "$pool" | head -n "$drop" | while read -r d; do
            log "$pool: removing superseded chain $(basename "$d")"
            rm -rf "$d"
          done
        }

        main() {
          log "=== ZFS Backup Started ==="
          local failed=0

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

            # Send off-host only when the backup mount is present. Prune chains
            # only after a send lands, so a failed run can never delete the last
            # good chain without having written a replacement.
            if [ "$have_mount" = true ]; then
              if backup_pool "$pool" "$snap_name"; then
                cleanup_old_chains "$pool"
              else
                log "ERROR: off-host backup of $pool FAILED"
                failed=1
              fi
            fi

            # Always prune old snapshots so the pool can't fill
            cleanup_old_snapshots "$pool"
          done

          log "=== ZFS Backup Completed ==="
          # Exit non-zero on send failure so systemd marks the unit failed. The
          # old script exited 0 unconditionally, which is why nothing noticed.
          return $failed
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
        # A Type=oneshot inherits DefaultTimeoutStartSec (90s on Ubuntu). That
        # never mattered while the send shipped 165 bytes and finished in two
        # seconds; a real full send is hours (~1.4 TB over the routed NAS hop),
        # so without this systemd would SIGTERM it mid-stream every night.
        TimeoutStartSec=infinity
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
    # Size, not just presence. The previous check only asked whether a recent
    # file existed, so it passed green for months on 165-byte streams that
    # contained nothing. A real daily incremental is hundreds of MB.
    backup-recent = {
      type = "command";
      command = "test \"$(find /mnt/backup/$(hostname -s) -name '*.zfs' -mtime -2 -printf '%s\\n' 2>/dev/null | awk '{s+=$1} END {print s+0}')\" -gt 1048576";
      timeout = 15;
    };
  };
}

# NixFleet iSCSI Initiator Module
#
# Prepares an Ubuntu host to act as an iSCSI initiator so Kubernetes CSI drivers
# (democratic-csi / synology-csi) can attach iSCSI-backed PersistentVolumes that
# the Synology NAS provisions as btrfs LUNs.
#
# What it does (declaratively, on each activation):
#   - installs the distro `open-iscsi` package (apt) if missing
#   - persists the `iscsi_tcp` kernel module load across boots (modules-load.d)
#   - generates a unique InitiatorName if one isn't present
#   - enables + starts iscsid (and open-iscsi.service)
#   - health-checks that iscsid is active
#
# open-iscsi is intentionally taken from the distro (apt) rather than nixpkgs so
# that iscsid integrates with the host's udev, systemd, and (kernel-shipped)
# iscsi_tcp module. A nix-store iscsid would not wire up cleanly to those.
#
# Usage:
#   imports = [ ../modules/iscsi.nix ];
#   nixfleet.modules.iscsi.enable = true;
{
  config,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.iscsi;
in
{
  options.nixfleet.modules.iscsi = {
    enable = lib.mkEnableOption "iSCSI initiator (open-iscsi) for CSI-attached volumes";

    replacementTimeout = lib.mkOption {
      type = lib.types.ints.positive;
      default = 600;
      description = ''
        iSCSI node.session.timeo.replacement_timeout in seconds — how long the
        initiator waits for a stalled target before failing in-flight I/O. On
        single-path nodes raise this above the expected worst-case target stall
        so a transient Synology DSM hiccup makes I/O hang-and-resume instead of
        escalating to SCSI-offline → btrfs forced-readonly (the 2026-07-23
        gastown outage, nix-fleet-hosts-k3x). Applied on activation to
        iscsid.conf, to existing node records, and — via each session's
        recovery_tmo in sysfs — to sessions that are already logged in, so it
        takes effect immediately without a re-login (a re-login would drop live
        CSI PVCs, so activation deliberately never forces one).

        Default 600 (not open-iscsi's upstream 120): on the 2026-08-13 znas blip
        gti at 600 logged "operational after recovery" while gtr-153 at 120 hit
        "session recovery timed out after 120 secs" and forced the
        monitoring/prometheus-server btrfs LUN readonly. btrfs never recovers
        from that in place — it needs a full unmount/remount (pod delete →
        CSI detach/reattach), so erroring early is strictly worse than hanging.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # Load the iSCSI TCP transport module on every boot.
    nixfleet.files."/etc/modules-load.d/iscsi_tcp.conf" = {
      mode = "0644";
      owner = "root";
      group = "root";
      text = ''
        # Managed by NixFleet iscsi module — iSCSI TCP transport
        iscsi_tcp
      '';
    };

    nixfleet.hooks.postActivate = ''
      # --- iSCSI initiator setup (NixFleet iscsi module) ---
      # Distro open-iscsi provides iscsid + iscsiadm + iscsi-iname, integrated
      # with the host kernel/udev. Install it unless it is *fully* installed:
      # a removed-but-config-remains ("rc") package still appears in `dpkg -l`,
      # so match the leading "ii" status column to require the installed state.
      if ! dpkg -l open-iscsi 2>/dev/null | grep -q '^ii'; then
        echo "iscsi: installing open-iscsi"
        DEBIAN_FRONTEND=noninteractive apt-get install -y open-iscsi \
          || echo "iscsi: WARNING apt-get install open-iscsi failed"
      fi

      # Ensure a unique InitiatorName exists (CSI attach needs one per node).
      if [ ! -s /etc/iscsi/initiatorname.iscsi ]; then
        iname=""
        for c in iscsi-iname /usr/sbin/iscsi-iname /sbin/iscsi-iname; do
          if command -v "$c" >/dev/null 2>&1; then iname="$("$c" 2>/dev/null || true)"; fi
          [ -n "$iname" ] && break
        done
        if [ -z "$iname" ]; then
          # Fallback IQN if iscsi-iname is unavailable (don't fail activation).
          iname="iqn.2004-10.com.ubuntu:01:$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
          echo "iscsi: WARNING iscsi-iname unavailable, using fallback IQN"
        fi
        mkdir -p /etc/iscsi
        install -m 0600 /dev/null /etc/iscsi/initiatorname.iscsi
        printf 'InitiatorName=%s\n' "$iname" > /etc/iscsi/initiatorname.iscsi
        echo "iscsi: set InitiatorName=$iname"
      fi

      # Load the transport now (modules-load.d covers subsequent boots).
      modprobe iscsi_tcp 2>/dev/null || true

      # Enable + start the iSCSI services.
      systemctl enable --now iscsid.service 2>/dev/null || true
      systemctl enable --now open-iscsi.service 2>/dev/null || true
      echo "iscsi: iscsid is $(systemctl is-active iscsid.service 2>/dev/null)"

      # --- session recovery timeout (NixFleet iscsi module) ---
      # replacement_timeout bounds how long the initiator waits for a stalled
      # target before failing I/O. The 120s default let a ~292s Synology DSM
      # stall on 2026-07-23 escalate to SCSI-offline → btrfs forced-readonly on
      # the gastown LUNs (nix-fleet-hosts-k3x). A larger value makes I/O hang and
      # resume when the target returns rather than erroring the filesystem RO.
      RT=${toString cfg.replacementTimeout}
      conf=/etc/iscsi/iscsid.conf
      if [ -f "$conf" ]; then
        if grep -qE '^[#[:space:]]*node\.session\.timeo\.replacement_timeout' "$conf"; then
          sed -i -E "s|^[#[:space:]]*node\.session\.timeo\.replacement_timeout.*|node.session.timeo.replacement_timeout = $RT|" "$conf"
        else
          printf 'node.session.timeo.replacement_timeout = %s\n' "$RT" >> "$conf"
        fi
        echo "iscsi: iscsid.conf replacement_timeout=$RT (new sessions)"
      fi
      # Update existing node records so the value takes effect on next login.
      # Deliberately NOT re-logging-in here — that would drop live CSI PVCs.
      # New sessions pick it up at login; already-live ones are handled below.
      if command -v iscsiadm >/dev/null 2>&1; then
        iscsiadm -m node -o update -n node.session.timeo.replacement_timeout -v "$RT" 2>/dev/null \
          && echo "iscsi: node records replacement_timeout=$RT (new logins)" \
          || true
      fi
      # Already-logged-in sessions keep the OLD value — which is exactly the
      # ones holding live PVCs, so without this the new timeout protects
      # nothing until a reboot. recovery_tmo is the running session's copy of
      # replacement_timeout and is writable, so set it in place; no re-login,
      # no I/O interruption. (2026-08-13: had to do this by hand fleet-wide
      # after raising the default, because activation alone changed nothing.)
      rtn=0
      for s in /sys/class/iscsi_session/session*/recovery_tmo; do
        [ -e "$s" ] || continue
        if echo "$RT" > "$s" 2>/dev/null; then
          rtn=$((rtn + 1))
        else
          echo "iscsi: WARNING could not set $s=$RT"
        fi
      done
      if [ "$rtn" -gt 0 ]; then
        echo "iscsi: recovery_tmo=$RT on $rtn live session(s)"
      fi
    '';

    nixfleet.healthChecks.iscsid = {
      type = "command";
      command = "systemctl is-active iscsid.service";
      timeout = 5;
    };
  };
}

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
    '';

    nixfleet.healthChecks.iscsid = {
      type = "command";
      command = "systemctl is-active iscsid.service";
      timeout = 5;
    };
  };
}

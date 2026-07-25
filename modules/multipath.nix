# NixFleet multipath module
#
# Blacklists Synology iSCSI LUNs from Linux's dm-multipath auto-claim.
#
# Root cause (2026-07-25 gastown-town readonly incident, see the
# gastown-readonly-multipath-2026-07-25 memory for the full writeup): DSM's
# iSCSI target service presents each mapped LUN at two SCSI LUN-numbers
# within a single session (ALUA active/active), independent of how many
# network portals/sessions the initiator establishes. synology-csi's own
# multipath handling (pkg/driver/nodeserver.go getVolumeMountPath) only
# switches to mounting via /dev/mapper/<name> when it sees more than one
# iSCSI *session* for a target — it has no visibility into DSM's
# within-session dual-LUN-number behavior. Meanwhile udev's stock multipath
# rules independently notice the two raw SCSI devices share a WWN and let
# multipathd claim them into a dm-multipath group regardless. The driver
# then tries to mount its own raw single-path by-path guess directly, but
# the kernel refuses because that device is now a claimed multipath member —
# producing a persistent, misleading "already mounted or mount point busy"
# error with zero visible mounts anywhere. That took hours to isolate  (had
# to bypass kubelet and mount the raw device by hand to prove it wasn't a
# kubelet/CSI-container mount-namespace issue) and briefly put gastown-town
# on the wrong, unrelated LUN, forcing it read-only.
#
# Fix: blacklist Synology's LUN vendor/product in multipath.conf so
# multipathd never claims them, leaving the raw device free for the driver's
# expected direct single-path mount. Nothing on this fleet has a genuine
# need for multipath, so this is scoped to the vendor rather than disabling
# multipath auto-detection fleet-wide.
{
  config,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.multipath;
in
{
  options.nixfleet.modules.multipath = {
    enable = lib.mkEnableOption "multipath-tools with a Synology LUN blacklist (prevents multipathd claiming devices synology-csi expects raw single-path access to)";
  };

  config = lib.mkIf cfg.enable {
    nixfleet.files."/etc/multipath.conf" = {
      mode = "0644";
      owner = "root";
      group = "root";
      text = ''
        # Managed by NixFleet multipath module — see modules/multipath.nix
        # for the incident this guards against.
        blacklist {
          device {
            vendor  "SYNOLOGY"
            product "Storage"
          }
        }
      '';
    };

    nixfleet.hooks.postActivate = ''
      # --- multipath blacklist (NixFleet multipath module) ---
      if ! dpkg -l multipath-tools 2>/dev/null | grep -q '^ii'; then
        echo "multipath: installing multipath-tools"
        DEBIAN_FRONTEND=noninteractive apt-get install -y multipath-tools \
          || echo "multipath: WARNING apt-get install multipath-tools failed"
      fi

      systemctl enable --now multipathd.service 2>/dev/null || true
      echo "multipath: multipathd is $(systemctl is-active multipathd.service 2>/dev/null)"

      # Reload so the blacklist takes effect without a reboot, and release
      # any Synology maps multipathd claimed before this module was applied.
      # `multipath -f` on an actively-mounted map fails harmlessly (does not
      # force-unmount), so this is safe to run unconditionally on every
      # activation.
      if command -v multipath >/dev/null 2>&1; then
        multipathd reconfigure 2>/dev/null || true
        for m in $(multipath -ll 2>/dev/null | awk '/^mpath/{print $1}'); do
          multipath -f "$m" 2>/dev/null || true
        done
        echo "multipath: reconfigured, active maps: $(multipath -ll 2>/dev/null | grep -c '^mpath')"
      fi
    '';

    nixfleet.healthChecks.multipathd = {
      type = "command";
      command = "systemctl is-active multipathd.service";
      timeout = 5;
    };
  };
}

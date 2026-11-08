# This module loads a fixed copy of the default containerd AppArmor profile on the k0s nodes.
# The profile name is cri-containerd.apparmor.d.
#
# AppArmor 5.0.2 on Ubuntu 26.04 gives container tasks a stacked label.
# The stock profile does not match that label.
# So processes in one container cannot signal each other.
#
# This makes gosu hang.
# The Postgres, mongo and redis images do not start.
# The profile file gives more detail.
#
# AppArmor stays enforced.
# The pods need no change.
# The activation hook replaces the loaded profile.
# The running containers get the fix without a restart.
# A container that is already stuck needs a pod restart.
{ config, lib, ... }:
let
  cfg = config.nixfleet.containerdAppArmor;
  path = "/etc/apparmor.d/cri-containerd.apparmor.d";
in
{
  options.nixfleet.containerdAppArmor.enable = lib.mkEnableOption "the fixed containerd AppArmor profile for AppArmor 5 stacked labels";

  config = lib.mkIf cfg.enable {
    nixfleet.files.${path} = {
      mode = "0644";
      owner = "root";
      group = "root";
      text = builtins.readFile ./containerd-apparmor/cri-containerd.apparmor.d;
    };

    nixfleet.hooks.postActivate = ''
      # --- Load the containerd AppArmor profile (NixFleet containerd-apparmor module) ---
      if command -v apparmor_parser >/dev/null && aa-enabled -q 2>/dev/null; then
        apparmor_parser -r -K ${path} \
          || echo "containerd-apparmor: WARNING apparmor_parser failed"
      fi
    '';
  };
}

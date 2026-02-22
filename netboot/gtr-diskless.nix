# NixFleet Netboot Image — GTR Diskless
# Extends the GTR netboot image with NFS-backed persistent storage.
# The GTI server exports /srv/netboot/persistent/gtr which is mounted
# as /persistent on the netbooted GTR, with /home bind-mounted from it.
#
# Build: nix build .#netboot-gtr-diskless
{ pkgs, lib, ... }:

{
  imports = [
    ./gtr.nix
  ];

  # ============================================================================
  # NFS client support
  # ============================================================================
  boot.supportedFilesystems = [
    "nfs"
    "nfs4"
  ];

  environment.systemPackages = with pkgs; [
    nfs-utils
  ];

  # ============================================================================
  # NFS mount for persistent storage
  # ============================================================================
  fileSystems."/persistent" = {
    device = "192.168.3.131:/srv/netboot/persistent/gtr";
    fsType = "nfs4";
    options = [
      "x-systemd.automount"
      "x-systemd.idle-timeout=600"
      "noauto"
      "nofail"
      "_netdev"
    ];
  };

  # ============================================================================
  # Bind mount /home from persistent storage
  # ============================================================================
  system.activationScripts.persistentHome = lib.stringAfter [ "users" ] ''
    # Wait briefly for NFS automount to be ready
    mkdir -p /persistent/home

    # Bind mount persistent home if not already mounted
    if ! mountpoint -q /home 2>/dev/null; then
      mount --bind /persistent/home /home || true
    fi
  '';

  # ============================================================================
  # Hostname override for diskless mode
  # ============================================================================
  networking.hostName = lib.mkForce "gtr-diskless";
}

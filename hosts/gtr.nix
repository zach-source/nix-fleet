# GTR — NixOS netboot target (192.168.3.31)
# Ubuntu 24.04 host managed by NixFleet.
# Boots via PXE from GTI, runs k0s worker node.
{ pkgs, ... }:

{
  imports = [
    ../modules/base-packages.nix
    ../modules/tpm2-unlock.nix
    ../modules/backup.nix
  ];

  nixfleet = {
    host = {
      name = "gtr";
      base = "ubuntu";
      addr = "192.168.3.31";
    };

    # ============================================================================
    # TPM2 auto-unlock for ZFS-on-LUKS keystore
    # ============================================================================
    modules.tpm2Unlock.enable = true;
  };
}

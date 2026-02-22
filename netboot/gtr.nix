# NixFleet Netboot Image — GTR (AMD ROCm box)
# Imports common netboot base and adds AMD/ROCm hardware support.
#
# Build: nix build .#netboot-gtr
# Result: bzImage, initrd, nix-store.squashfs, cmdline
{ pkgs, lib, ... }:

{
  imports = [
    ./common.nix
  ];

  # ============================================================================
  # AMD GPU / ROCm
  # ============================================================================
  boot.initrd.kernelModules = [ "amdgpu" ];

  boot.kernelParams = [
    "amdgpu.ppfeaturemask=0xffffffff"
  ];

  hardware.graphics = {
    enable = true;
    extraPackages = with pkgs; [
      rocmPackages.clr.icd
    ];
  };

  # AMD microcode
  hardware.cpu.amd.updateMicrocode = true;

  # ROCm environment
  environment.variables = {
    HSA_OVERRIDE_GFX_VERSION = "11.0.0";
  };

  # ============================================================================
  # ROCm / GPU packages
  # ============================================================================
  environment.systemPackages = with pkgs; [
    rocmPackages.rocm-smi
    rocmPackages.rocminfo
    radeontop
    nvtopPackages.amd
    llama-cpp
  ];

  # ============================================================================
  # Hostname
  # ============================================================================
  networking.hostName = "gtr-netboot";
}

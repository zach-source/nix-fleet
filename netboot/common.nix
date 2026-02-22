# NixFleet Netboot Base Configuration
# Shared base for all netboot images — provides the same environment as
# the USB installer ISOs but using the NixOS netboot module instead.
#
# Produces: system.build.kernel, system.build.netbootRamdisk, system.build.squashfsStore
{ pkgs, lib, ... }:

{
  imports = [
    # NixOS netboot module — provides kernel, initrd, and squashfs store builds
    <nixpkgs/nixos/modules/installer/netboot/netboot.nix>
  ];

  # Use zstd for squashfs — fast decompression on the client
  netboot.squashfsCompression = "zstd -Xcompression-level 19";

  # ============================================================================
  # Networking
  # ============================================================================
  networking.useDHCP = true;

  # ============================================================================
  # SSH
  # ============================================================================
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "yes";
      PasswordAuthentication = true;
    };
  };

  # ============================================================================
  # Users
  # ============================================================================
  users.users.root = {
    initialPassword = "nixfleet";
  };

  users.users.nixfleet = {
    isNormalUser = true;
    initialPassword = "nixfleet";
    extraGroups = [
      "wheel"
      "video"
      "render"
    ];
    openssh.authorizedKeys.keys = [ ];
  };

  security.sudo.wheelNeedsPassword = false;

  # ============================================================================
  # Packages — mirrors the USB installer ISO package set
  # ============================================================================
  environment.systemPackages = with pkgs; [
    # NixFleet CLI (built from this flake, passed via specialArgs)
    # nixfleet  # uncomment when specialArgs provides it

    # Version control
    git

    # Secrets / encryption
    age
    ssh-to-age

    # Editors
    vim
    nano
    helix

    # Disk tools
    parted
    gptfdisk
    dosfstools
    e2fsprogs
    btrfs-progs
    zfs

    # Network tools
    curl
    wget
    openssh
    iproute2
    dig
    tcpdump
    nmap
    ethtool

    # System tools
    htop
    iotop
    lsof
    pciutils
    usbutils
    dmidecode
    smartmontools
    nvme-cli

    # General utilities
    jq
    ripgrep
    fd
    bat
    tmux
    tree
    file
    unzip
    gzip
    xz
  ];

  # ============================================================================
  # Nix settings
  # ============================================================================
  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    trusted-users = [
      "root"
      "nixfleet"
    ];
  };

  # ============================================================================
  # Misc
  # ============================================================================
  time.timeZone = "America/Chicago";

  # Required by NixOS
  system.stateVersion = "24.11";
}

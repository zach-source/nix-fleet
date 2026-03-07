# NixFleet Autoinstall Config Generator
#
# Generates per-host Ubuntu autoinstall YAML (user-data + meta-data)
# for unattended installation via PXE + cloud-init nocloud datasource.
#
# Usage:
#   import ./installer/autoinstall.nix {
#     inherit lib pkgs;
#     hostConfig = nixfleetConfigurations.gtr.config;
#     sshPubKey = builtins.readFile ../secrets/deploy.pub;
#     installerHost = "192.168.3.131";
#     installerPort = 8889;
#     luksPassphrase = "nixfleet-init-gtr";
#   }
{
  lib,
  pkgs,
  hostConfig,
  sshPubKey,
  installerHost ? "192.168.3.131",
  installerPort ? 8889,
  luksPassphrase ? "nixfleet-init-${hostConfig.nixfleet.host.name}",
  targetDisk ? "/dev/sda",
}:

let
  cfg = hostConfig.nixfleet;
  hostname = cfg.host.name;
  addr = cfg.host.addr;
  baseUrl = "http://${installerHost}:${toString installerPort}";

  # Escape single quotes in SSH key for YAML
  sshKey = lib.removeSuffix "\n" (builtins.readFile sshPubKey);

  autoinstallYaml = pkgs.writeText "user-data" ''
    #cloud-config
    autoinstall:
      version: 1

      locale: en_US.UTF-8
      timezone: America/Chicago
      keyboard:
        layout: us

      identity:
        hostname: ${hostname}
        username: ubuntu
        # Locked password — SSH key only
        password: "!"

      ssh:
        install-server: true
        authorized-keys:
          - "${sshKey}"
        allow-pw: false

      # Use early-commands for custom ZFS-on-LUKS partitioning
      # This runs BEFORE the installer touches disks
      early-commands:
        - >-
          curl -sSfL ${baseUrl}/common/zfs-partition.sh |
          DISK=${targetDisk}
          HOSTNAME=${hostname}
          LUKS_PASSPHRASE="${luksPassphrase}"
          bash

      # Tell autoinstall not to touch storage (we handle it in early-commands)
      storage:
        config: []

      # Minimal apt packages needed before Nix takes over
      packages:
        - curl
        - xz-utils
        - openssh-server
        - tpm2-tools
        - zfsutils-linux

      # Network: use DHCP (will be configured by NixFleet later)
      network:
        version: 2
        ethernets:
          id0:
            match:
              name: "en*"
            dhcp4: true

      # Late commands: run the NixFleet bootstrap
      late-commands:
        - >-
          curtin in-target -- bash -c
          'curl -sSfL ${baseUrl}/common/nixfleet-late-commands.sh |
          bash -s --
          --deploy-user deploy
          --ssh-key "${sshKey}"
          --hostname ${hostname}
          --luks-passphrase "${luksPassphrase}"
          --mgmt-host "${installerHost}"'
  '';

  metaData = pkgs.writeText "meta-data" ''
    instance-id: ${hostname}
    local-hostname: ${hostname}
  '';
in
pkgs.runCommand "autoinstall-${hostname}" { } ''
  mkdir -p $out
  cp ${autoinstallYaml} $out/user-data
  cp ${metaData} $out/meta-data
''

# Base module - common configuration for all hosts
{
  config,
  pkgs,
  lib,
  ...
}:

{
  nixfleet.packages = with pkgs; [
    # Essential tools
    coreutils
    findutils
    gnugrep
    gnused
    gawk

    # Networking
    curl
    wget
    openssh
    netcat

    # Monitoring
    htop
    iotop
    sysstat

    # Debugging
    strace
    lsof
    tcpdump

    # Editors
    vim
    nano
  ];

  # Standard directories
  nixfleet.directories = {
    "/var/lib/nixfleet" = {
      mode = "0750";
      owner = "root";
      group = "root";
    };
    "/etc/.nixfleet/staging" = {
      mode = "0700";
      owner = "root";
      group = "root";
    };
  };
}

# GTI — NVIDIA box (192.168.3.131)
# Ubuntu 24.04 host managed by NixFleet.
# Serves as the PXE netboot server for GTR and LAN DNS server.
{ pkgs, ... }:

{
  imports = [
    ../modules/netboot-server.nix
    ../modules/dns.nix
  ];

  nixfleet = {
    host = {
      name = "gti";
      base = "ubuntu";
      addr = "192.168.3.131";
    };

    packages = with pkgs; [
      git
      htop
      curl
      jq
      tmux
      vim
      nfs-kernel-server
    ];

    # ============================================================================
    # Netboot server — serves GTR NixOS image over PXE
    # ============================================================================
    modules.netbootServer = {
      enable = true;
      target = "gtr";
      httpPort = 8888;
    };

    # ============================================================================
    # DNS server — Unbound with local records + DoT upstream
    # ============================================================================
    modules.dns = {
      enable = true;
      domain = "stigen.lan";
      localRecords = {
        gtr = "192.168.3.31";
        gti = "192.168.3.131";
        netboot = "192.168.3.131";
      };
      adblock.enable = true;
    };

    # ============================================================================
    # Firewall rules for netboot services
    # ============================================================================
    files = {
      "/etc/nixfleet/netboot-firewall.sh" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          # Firewall rules for PXE netboot services
          # Run once after deployment to open required ports

          set -euo pipefail

          LAN="192.168.3.0/24"

          # Pixiecore HTTP (boot artifacts)
          iptables -C INPUT -p tcp --dport 8888 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p tcp --dport 8888 -s "$LAN" -j ACCEPT

          # DHCP proxy
          iptables -C INPUT -p udp --dport 67 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p udp --dport 67 -s "$LAN" -j ACCEPT

          # TFTP
          iptables -C INPUT -p udp --dport 69 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p udp --dport 69 -s "$LAN" -j ACCEPT

          # PXE
          iptables -C INPUT -p udp --dport 4011 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p udp --dport 4011 -s "$LAN" -j ACCEPT

          # NFS (for diskless mode)
          iptables -C INPUT -p tcp --dport 2049 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p tcp --dport 2049 -s "$LAN" -j ACCEPT

          # DNS (UDP + TCP)
          iptables -C INPUT -p udp --dport 53 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p udp --dport 53 -s "$LAN" -j ACCEPT
          iptables -C INPUT -p tcp --dport 53 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p tcp --dport 53 -s "$LAN" -j ACCEPT

          echo "Netboot + DNS firewall rules applied"
        '';
      };

      # NFS exports for diskless mode
      "/etc/exports.d/netboot.exports" = {
        mode = "0644";
        owner = "root";
        group = "root";
        text = ''
          /srv/netboot/persistent/gtr 192.168.3.31/32(rw,sync,no_subtree_check,no_root_squash)
        '';
      };
    };

    # ============================================================================
    # Directories for netboot persistent storage (diskless mode)
    # ============================================================================
    directories = {
      "/srv/netboot/persistent/gtr" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
      "/srv/netboot/persistent/gtr/home" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
      "/etc/exports.d" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
    };

    # ============================================================================
    # Systemd units
    # ============================================================================
    systemd.units = {
      # Apply firewall rules on boot
      "nixfleet-netboot-firewall.service" = {
        text = ''
          [Unit]
          Description=Apply netboot firewall rules
          After=network-online.target
          Wants=network-online.target

          [Service]
          Type=oneshot
          ExecStart=/etc/nixfleet/netboot-firewall.sh
          RemainAfterExit=true

          [Install]
          WantedBy=multi-user.target
        '';
        enabled = true;
      };
    };

    # ============================================================================
    # Health checks
    # ============================================================================
    healthChecks = {
      nfs-server = {
        type = "command";
        command = "systemctl is-active nfs-server || systemctl is-active nfs-kernel-server";
        timeout = 5;
      };
    };

    hooks = {
      postActivate = ''
        # Re-export NFS shares after config changes
        exportfs -ra 2>/dev/null || true
        echo "GTI netboot server activation complete"
      '';
    };
  };
}

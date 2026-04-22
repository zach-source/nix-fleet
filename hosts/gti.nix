# GTI — NVIDIA box (192.168.3.131)
# Ubuntu 24.04 host managed by NixFleet.
# Serves as the PXE netboot server for GTR and LAN DNS server.
{ pkgs, ... }:

{
  imports = [
    ../modules/netboot-server.nix
    ../modules/dns.nix
    ../modules/tpm2-unlock.nix
    ../modules/installer.nix
    ../modules/sysctl.nix
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
      nfs-utils
    ];

    # ============================================================================
    # TPM2 auto-unlock for ZFS-on-LUKS keystore
    # ============================================================================
    modules.tpm2Unlock.enable = true;

    # ============================================================================
    # Kernel tuning for k0s + JuiceFS CSI driver
    # The CSI plugin opens many inotify watchers at startup; Ubuntu's defaults
    # (max_user_instances=128, max_user_watches=8192) exhaust quickly under
    # mixed k8s workloads and surface as "too many open files" at CSI init.
    # ============================================================================
    modules.sysctl = {
      enable = true;
      settings = {
        # JuiceFS CSI + general k8s inotify headroom
        "fs.inotify.max_user_watches" = 524288;
        "fs.inotify.max_user_instances" = 8192;
        # Process-wide fd ceiling (containerd/kubelet already high, but
        # kernel cap keeps us out of per-process EMFILE territory)
        "fs.file-max" = 1048576;
        "fs.nr_open" = 1048576;
      };
    };

    # ============================================================================
    # Ubuntu installer — serves autoinstall configs for PXE installs
    # ============================================================================
    modules.installer = {
      enable = true;
      targets = [ "gtr" ];
    };

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
      accessControl = [
        "192.168.0.0/16"
        "10.244.0.0/16" # k0s pod network — CoreDNS forwards here
      ];
      localRecords = {
        gti = "192.168.3.131";
        gtr-150 = "192.168.3.133";
        gtr-151 = "192.168.3.132";
        gtr-152 = "192.168.3.134";
        gtr-153 = "192.168.3.130";
        netboot = "192.168.3.131";
        jetkvm-gti = "192.168.3.135";
        jetkvm-gtr = "192.168.3.120";
      };
      insecureDomains = [
        "cluster.local"
      ];
      adblock.enable = true;
      extraConfig = ''
        # k0s CoreDNS — Kubernetes service discovery (via NodePort 30053)
        forward-zone:
            name: "cluster.local"
            forward-tls-upstream: no
            forward-addr: 127.0.0.1@30053
      '';
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

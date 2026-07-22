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
    ../modules/ufw.nix
    ../modules/iscsi.nix
    ../modules/backup.nix
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

    # iSCSI initiator so the Synology CSI driver can attach btrfs-backed LUNs.
    modules.iscsi.enable = true;

    # Raise iSCSI session-recovery timeout well above the observed worst-case
    # Synology DSM stall (~292s on 2026-07-23) so a transient target hiccup makes
    # I/O hang-and-resume instead of escalating to btrfs forced-readonly on the
    # gastown LUNs. Single-path single-node: fast failover buys nothing here.
    # See nix-fleet-hosts-k3x. Takes effect on next iSCSI login (pod roll/reboot).
    modules.iscsi.replacementTimeout = 600;

    # nix-config module is for Determinate-Nix hosts (writes nix.custom.conf).
    # gti runs vanilla nix (require-sigs=false, connects as the trusted ztaylor
    # user, nix config in nix.conf.d/99-nixfleet-optimized.conf) — it has no
    # deploy-trust problem and doesn't `!include nix.custom.conf`, so the module
    # would be a no-op here. Disable it explicitly.
    modules.nixConfig.enable = false;

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
        # cloudflared (hostNetwork) + quic-go: the QUIC stack wants 7 MiB
        # UDP socket buffers to absorb bursty CF edge traffic. Ubuntu's
        # default rmem_max=212992 (208 KiB) trips a noisy warning and
        # caps throughput. 7340032 = 7 MiB matches quic-go's wanted size.
        "net.core.rmem_max" = 7340032;
        "net.core.wmem_max" = 7340032;
        # IP forwarding for WARP Connector — the cloudflared pod in
        # warp-connector mode forwards packets between Cloudflare's
        # edge and the LAN. k0s already enables ipv4.ip_forward at
        # cluster bring-up; we re-declare it here so the host stays
        # consistent on cold boot before k0scontroller starts.
        "net.ipv4.ip_forward" = 1;
        "net.ipv6.conf.all.forwarding" = 1;
      };
    };

    # ============================================================================
    # UFW rules — declared via NixFleet so they survive re-deploys.
    # The apply script inserts these at UFW position 1 so they win over
    # any pre-existing `limit`/deny rules. The port 22 entry below is
    # specifically there to outrank the legacy `limit 22/tcp` from
    # Ubuntu's default install: `limit` tarpit-drops new SYNs after 6
    # connections in 30s, which makes interactive ssh debugging from a
    # single LAN IP fail intermittently.
    # ============================================================================
    modules.ufw = {
      enable = true;
      rules = [
        {
          from = "192.168.0.0/16";
          port = 22;
          comment = "ssh from LAN — outrank legacy `limit 22/tcp`";
        }
        {
          from = "10.244.0.0/16";
          port = 6443;
          comment = "k8s API from cluster pod CIDR (incl. cloudflared egress)";
        }
        {
          from = "192.168.0.0/16";
          port = 6443;
          comment = "k8s API from LAN/WARP";
        }
        {
          from = "10.244.0.0/16";
          port = 8132;
          comment = "konnectivity-agent → server from pod CIDR";
        }
        {
          from = "10.244.0.0/16";
          port = 18080;
          comment = "archive-v6-proxy: pods → node-local apt IPv6-egress proxy";
        }
        {
          from = "192.168.0.0/16";
          port = 8132;
          comment = "konnectivity-agent → server from LAN (multi-node future)";
        }
      ];
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

      # WARP Connector reverse-path route applier.
      #
      # Background: the cloudflared-warp-connector pod creates a
      # `CloudflareWARP` TUN device with only its own /32 (e.g.
      # 100.96.0.13/32) attached. warp-svc's connector mode does NOT
      # install the wider 100.96.0.0/16 covering the rest of CF's
      # CGNAT range, so reply packets to other WARP clients
      # (100.96.0.x) miss the TUN and fall through to the default
      # route — out enp174s0f1 to the LAN gateway, which has no path
      # back to CGNAT and silently drops them.
      #
      # The pod's entrypoint also installs this route, but declaring
      # it at the host layer means: (a) survives any pod-side mishap,
      # (b) re-installs automatically when the pod restarts and the
      # TUN cycles, (c) shows up in `ip route` even if the pod isn't
      # the only thing managing routes long-term.
      "/etc/nixfleet/warp-route-apply.sh" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          # Loop forever, re-applying the route whenever the
          # CloudflareWARP TUN exists. `ip route replace` is
          # idempotent (no-op when route is identical). When the iface
          # goes away the kernel removes the route automatically; we
          # add it back next iteration.
          set -u
          while true; do
            if /usr/sbin/ip link show CloudflareWARP >/dev/null 2>&1; then
              /usr/sbin/ip route replace 100.96.0.0/16 dev CloudflareWARP 2>/dev/null || true
            fi
            sleep 30
          done
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

      # WARP Connector route maintainer — re-installs
      # 100.96.0.0/16 → CloudflareWARP whenever the TUN exists.
      # See /etc/nixfleet/warp-route-apply.sh for the full rationale.
      "nixfleet-warp-route.service" = {
        text = ''
          [Unit]
          Description=Maintain reverse-path route for WARP Connector CGNAT
          After=network-online.target
          Wants=network-online.target

          [Service]
          Type=simple
          ExecStart=/etc/nixfleet/warp-route-apply.sh
          Restart=always
          RestartSec=10s

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
      warp-route-applier = {
        type = "command";
        command = "systemctl is-active nixfleet-warp-route.service >/dev/null";
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

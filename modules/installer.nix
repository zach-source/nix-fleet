# NixFleet Ubuntu Installer Module
#
# Provides two services on the management host:
#   1. HTTP server — serves autoinstall configs + bootstrap scripts
#   2. PXE server — pixiecore in API mode for per-MAC boot decisions
#
# Supports two boot modes:
#   - install:  Unattended Ubuntu + NixFleet + ZFS-on-LUKS + TPM2
#   - recovery: Live Ubuntu with ZFS/LUKS/TPM2 tools
#
# Usage:
#   installer-setup              # Download Ubuntu ISO, extract boot files
#   installer-pxe add-install <mac> <host>  # Queue host for install
#   installer-pxe add-recovery <mac>        # Queue MAC for recovery boot
#   installer-pxe list                      # Show PXE targets
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.installer;
  pxeApiPort = cfg.pxeApiPort;
in
{
  options.nixfleet.modules.installer = {
    enable = lib.mkEnableOption "NixFleet Ubuntu installer server";

    httpPort = lib.mkOption {
      type = lib.types.port;
      default = 8889;
      description = "HTTP port for serving autoinstall configs and scripts";
    };

    pxeApiPort = lib.mkOption {
      type = lib.types.port;
      default = 8891;
      description = "Port for pixiecore API backend";
    };

    artifactsPath = lib.mkOption {
      type = lib.types.str;
      default = "/srv/installer";
      description = "Base directory where installer artifacts are served from";
    };

    targets = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Host names to serve autoinstall configs for";
    };

    targetDisk = lib.mkOption {
      type = lib.types.str;
      default = "/dev/sda";
      description = "Default target disk for ZFS partitioning";
    };

    luksPassphrase = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = ''
        Initial LUKS passphrase template. If empty, defaults to
        "nixfleet-init-<hostname>" per host. Replaced by TPM2 on first boot.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # ============================================================================
    # Required packages
    # ============================================================================
    nixfleet.packages = with pkgs; [
      python3
      pixiecore
    ];

    # ============================================================================
    # Directories
    # ============================================================================
    nixfleet.directories = {
      "${cfg.artifactsPath}" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
      "${cfg.artifactsPath}/common" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
      "${cfg.artifactsPath}/boot" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
      "/var/cache/nixfleet" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
    }
    // lib.listToAttrs (
      map (host: {
        name = "${cfg.artifactsPath}/${host}";
        value = {
          mode = "0755";
          owner = "root";
          group = "root";
        };
      }) cfg.targets
    );

    # ============================================================================
    # Files
    # ============================================================================
    nixfleet.files = {
      # Common scripts (served to target hosts during install)
      "${cfg.artifactsPath}/common/zfs-partition.sh" = {
        mode = "0644";
        owner = "root";
        group = "root";
        source = ../installer/zfs-partition.sh;
      };

      "${cfg.artifactsPath}/common/nixfleet-late-commands.sh" = {
        mode = "0644";
        owner = "root";
        group = "root";
        source = ../installer/nixfleet-late-commands.sh;
      };

      # ============================================================================
      # Pixiecore API backend
      # ============================================================================
      "/etc/nixfleet/pxe-api.py" = {
        mode = "0755";
        owner = "root";
        group = "root";
        source = ../installer/pxe-api.py;
        restartUnits = [ "nixfleet-installer-pxe-api.service" ];
      };

      # ============================================================================
      # Installer HTTP server script
      # ============================================================================
      "/etc/nixfleet/installer-server.sh" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          SERVE_DIR="${cfg.artifactsPath}"
          PORT="${toString cfg.httpPort}"

          if [ ! -d "$SERVE_DIR" ]; then
            echo "ERROR: Serve directory $SERVE_DIR does not exist" >&2
            exit 1
          fi

          echo "Starting NixFleet installer HTTP server"
          echo "  Directory: $SERVE_DIR"
          echo "  Port:      $PORT"
          echo "  Targets:   ${lib.concatStringsSep ", " cfg.targets}"
          echo ""
          echo "Autoinstall URLs:"
          ${lib.concatMapStringsSep "\n" (host: ''
            echo "  ${host}: http://$(hostname):$PORT/${host}/user-data"
          '') cfg.targets}

          cd "$SERVE_DIR"
          exec python3 -m http.server "$PORT" --bind 0.0.0.0
        '';
        restartUnits = [ "nixfleet-installer.service" ];
      };

      # ============================================================================
      # Ubuntu ISO setup script
      # ============================================================================
      "/usr/local/bin/installer-setup" = {
        mode = "0755";
        owner = "root";
        group = "root";
        source = ../installer/installer-setup.sh;
      };

      # ============================================================================
      # Installer update script (copies Nix-built autoinstall configs)
      # ============================================================================
      "/usr/local/bin/installer-update" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          usage() {
            echo "Usage: installer-update <nix-build-result>"
            echo ""
            echo "Copy installer artifacts from a nix build result."
            echo ""
            echo "Example:"
            echo "  nix build .#installer"
            echo "  installer-update ./result"
            exit 1
          }

          if [ $# -ne 1 ]; then
            usage
          fi

          RESULT="$1"
          DEST="${cfg.artifactsPath}"

          if [ ! -d "$RESULT" ]; then
            echo "ERROR: Result path '$RESULT' does not exist" >&2
            exit 1
          fi

          echo "Updating installer artifacts in $DEST"

          # Copy common scripts
          if [ -d "$RESULT/common" ]; then
            cp -L "$RESULT/common/"* "$DEST/common/"
            echo "  Updated common scripts"
          fi

          # Copy per-host configs
          ${lib.concatMapStringsSep "\n" (host: ''
            if [ -d "$RESULT/${host}" ]; then
              mkdir -p "$DEST/${host}"
              cp -L "$RESULT/${host}/"* "$DEST/${host}/"
              echo "  Updated ${host} autoinstall config"
            fi
          '') cfg.targets}

          echo ""
          echo "Artifacts updated. Restart the installer server if running:"
          echo "  systemctl restart nixfleet-installer"
        '';
      };

      # ============================================================================
      # PXE target management CLI
      # ============================================================================
      "/usr/local/bin/installer-pxe" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          TARGETS_FILE="${cfg.artifactsPath}/pxe-targets.json"

          # Initialize targets file if missing
          if [ ! -f "$TARGETS_FILE" ]; then
            echo "{}" > "$TARGETS_FILE"
          fi

          usage() {
            echo "Usage: installer-pxe <command> [args]"
            echo ""
            echo "Manage PXE boot targets for NixFleet installer."
            echo ""
            echo "Commands:"
            echo "  add-install <mac> <hostname>  Queue a host for Ubuntu + NixFleet install"
            echo "  add-recovery <mac>            Queue a MAC for recovery boot"
            echo "  remove <mac>                  Remove a MAC from the PXE queue"
            echo "  list                          Show current PXE targets"
            echo "  clear                         Remove all PXE targets"
            echo "  status                        Show service status"
            echo ""
            echo "Examples:"
            echo "  installer-pxe add-install aa:bb:cc:dd:ee:ff gtr"
            echo "  installer-pxe add-recovery 11:22:33:44:55:66"
            echo "  installer-pxe list"
            exit 1
          }

          normalize_mac() {
            echo "$1" | tr '[:upper:]' '[:lower:]' | tr '-' ':'
          }

          cmd="''${1:-help}"
          shift || true

          case "$cmd" in
            add-install)
              if [ $# -ne 2 ]; then
                echo "Usage: installer-pxe add-install <mac> <hostname>"
                exit 1
              fi
              MAC=$(normalize_mac "$1")
              HOST="$2"
              # Validate host has autoinstall config
              if [ ! -f "${cfg.artifactsPath}/$HOST/user-data" ]; then
                echo "WARNING: No autoinstall config found for '$HOST'"
                echo "  Expected: ${cfg.artifactsPath}/$HOST/user-data"
                echo "  Run 'installer-update' first to populate configs"
              fi
              # Add to targets
              tmp=$(mktemp)
              python3 -c "
          import json, sys
          with open('$TARGETS_FILE') as f: targets = json.load(f)
          targets['$MAC'] = {'mode': 'install', 'host': '$HOST'}
          with open('$tmp', 'w') as f: json.dump(targets, f, indent=2)
          "
              mv "$tmp" "$TARGETS_FILE"
              echo "Added install target: $MAC -> $HOST"
              echo "PXE boot the machine to begin installation"
              ;;

            add-recovery)
              if [ $# -ne 1 ]; then
                echo "Usage: installer-pxe add-recovery <mac>"
                exit 1
              fi
              MAC=$(normalize_mac "$1")
              tmp=$(mktemp)
              python3 -c "
          import json
          with open('$TARGETS_FILE') as f: targets = json.load(f)
          targets['$MAC'] = {'mode': 'recovery'}
          with open('$tmp', 'w') as f: json.dump(targets, f, indent=2)
          "
              mv "$tmp" "$TARGETS_FILE"
              echo "Added recovery target: $MAC"
              echo "PXE boot the machine to enter recovery mode"
              ;;

            remove)
              if [ $# -ne 1 ]; then
                echo "Usage: installer-pxe remove <mac>"
                exit 1
              fi
              MAC=$(normalize_mac "$1")
              tmp=$(mktemp)
              python3 -c "
          import json
          with open('$TARGETS_FILE') as f: targets = json.load(f)
          targets.pop('$MAC', None)
          with open('$tmp', 'w') as f: json.dump(targets, f, indent=2)
          "
              mv "$tmp" "$TARGETS_FILE"
              echo "Removed: $MAC"
              ;;

            list)
              echo "PXE Boot Targets:"
              echo "================="
              python3 -c "
          import json
          with open('$TARGETS_FILE') as f: targets = json.load(f)
          if not targets:
              print('  (none)')
          for mac, cfg in sorted(targets.items()):
              mode = cfg.get('mode', '?')
              host = cfg.get('host', '-')
              if mode == 'install':
                  print(f'  {mac}  INSTALL -> {host}')
              elif mode == 'recovery':
                  print(f'  {mac}  RECOVERY')
              else:
                  print(f'  {mac}  {mode}')
          "
              ;;

            clear)
              echo "{}" > "$TARGETS_FILE"
              echo "All PXE targets cleared"
              ;;

            status)
              echo "NixFleet Installer Status"
              echo "========================="
              echo ""
              echo "Services:"
              echo "  HTTP server:   $(systemctl is-active nixfleet-installer 2>/dev/null || echo 'inactive')"
              echo "  PXE API:       $(systemctl is-active nixfleet-installer-pxe-api 2>/dev/null || echo 'inactive')"
              echo "  Pixiecore:     $(systemctl is-active nixfleet-installer-pxe 2>/dev/null || echo 'inactive')"
              echo ""
              echo "Boot files:"
              if [ -f "${cfg.artifactsPath}/boot/vmlinuz" ]; then
                echo "  vmlinuz: $(ls -lh ${cfg.artifactsPath}/boot/vmlinuz | awk '{print $5}')"
                echo "  initrd:  $(ls -lh ${cfg.artifactsPath}/boot/initrd 2>/dev/null | awk '{print $5}' || echo 'MISSING')"
              else
                echo "  NOT INSTALLED — run 'installer-setup' first"
              fi
              echo ""
              echo "Targets:"
              installer-pxe list 2>/dev/null | tail -n +2
              ;;

            *)
              usage
              ;;
          esac
        '';
      };

      # ============================================================================
      # Firewall rules for installer services
      # ============================================================================
      "/etc/nixfleet/installer-firewall.sh" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail
          LAN="192.168.3.0/24"

          # Installer HTTP server
          iptables -C INPUT -p tcp --dport ${toString cfg.httpPort} -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p tcp --dport ${toString cfg.httpPort} -s "$LAN" -j ACCEPT

          # DHCP proxy (pixiecore)
          iptables -C INPUT -p udp --dport 67 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p udp --dport 67 -s "$LAN" -j ACCEPT

          # TFTP (pixiecore)
          iptables -C INPUT -p udp --dport 69 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p udp --dport 69 -s "$LAN" -j ACCEPT

          # PXE proxy (pixiecore)
          iptables -C INPUT -p udp --dport 4011 -s "$LAN" -j ACCEPT 2>/dev/null || \
            iptables -A INPUT -p udp --dport 4011 -s "$LAN" -j ACCEPT

          echo "Installer firewall rules applied (HTTP ${toString cfg.httpPort}, DHCP, TFTP, PXE)"
        '';
      };
    };

    # ============================================================================
    # Systemd services
    # ============================================================================

    # HTTP server for autoinstall configs + scripts
    nixfleet.systemd.units."nixfleet-installer.service" = {
      text = ''
        [Unit]
        Description=NixFleet Installer HTTP Server
        After=network-online.target
        Wants=network-online.target

        [Service]
        Type=simple
        ExecStart=/etc/nixfleet/installer-server.sh
        Restart=on-failure
        RestartSec=10

        [Install]
        WantedBy=multi-user.target
      '';
      enabled = true;
    };

    # Pixiecore API backend (decides what to boot per MAC)
    nixfleet.systemd.units."nixfleet-installer-pxe-api.service" = {
      text = ''
        [Unit]
        Description=NixFleet Pixiecore API Backend
        After=network-online.target
        Wants=network-online.target

        [Service]
        Type=simple
        ExecStart=${pkgs.python3}/bin/python3 /etc/nixfleet/pxe-api.py \
          --port ${toString pxeApiPort} \
          --boot-dir ${cfg.artifactsPath}/boot \
          --targets-file ${cfg.artifactsPath}/pxe-targets.json \
          --installer-url http://${config.nixfleet.host.addr}:${toString cfg.httpPort}
        Restart=on-failure
        RestartSec=5

        [Install]
        WantedBy=multi-user.target
      '';
      enabled = true;
    };

    # Pixiecore in API mode (queries our API backend per PXE request)
    nixfleet.systemd.units."nixfleet-installer-pxe.service" = {
      text = ''
        [Unit]
        Description=NixFleet Installer PXE Server (pixiecore)
        After=nixfleet-installer-pxe-api.service
        Requires=nixfleet-installer-pxe-api.service

        [Service]
        Type=simple
        ExecStart=${pkgs.pixiecore}/bin/pixiecore api http://localhost:${toString pxeApiPort} \
          --dhcp-no-bind \
          --log-timestamps
        Restart=on-failure
        RestartSec=10

        # Needs raw socket access for DHCP/TFTP
        AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW
        NoNewPrivileges=false

        [Install]
        WantedBy=multi-user.target
      '';
      enabled = true;
    };

    # ============================================================================
    # Health checks
    # ============================================================================
    nixfleet.healthChecks.installer-server = {
      type = "command";
      command = "systemctl is-active nixfleet-installer";
      timeout = 5;
    };

    nixfleet.healthChecks.installer-pxe = {
      type = "command";
      command = "systemctl is-active nixfleet-installer-pxe";
      timeout = 5;
    };
  };
}

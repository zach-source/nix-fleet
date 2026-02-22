# NixFleet Netboot Server Module
# Runs pixiecore as a DHCP proxy + HTTP server to PXE boot target machines.
# Artifacts (kernel, initrd, cmdline) are read from disk at runtime so
# updating images only requires running netboot-update + service restart,
# not a full NixFleet redeploy.
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.netbootServer;
in
{
  options.nixfleet.modules.netbootServer = {
    enable = lib.mkEnableOption "NixFleet PXE netboot server (pixiecore)";

    artifactsPath = lib.mkOption {
      type = lib.types.str;
      default = "/srv/netboot/artifacts";
      description = "Base directory where kernel/initrd/cmdline artifacts are stored";
    };

    httpPort = lib.mkOption {
      type = lib.types.port;
      default = 8888;
      description = "HTTP port for pixiecore to serve boot artifacts";
    };

    dhcpNoBind = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Run in DHCP proxy mode (don't bind to port 67, works alongside existing DHCP)";
    };

    target = lib.mkOption {
      type = lib.types.str;
      default = "gtr";
      description = "Name of the target subdirectory under artifactsPath to serve";
    };

    debug = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable verbose/debug logging for pixiecore";
    };
  };

  config = lib.mkIf cfg.enable {
    # ============================================================================
    # Required packages
    # ============================================================================
    nixfleet.packages = [ pkgs.pixiecore ];

    # ============================================================================
    # Directories
    # ============================================================================
    nixfleet.directories = {
      "${cfg.artifactsPath}" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
      "${cfg.artifactsPath}/${cfg.target}" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
    };

    # ============================================================================
    # Netboot server wrapper script
    # Reads kernel, initrd, and cmdline from the artifacts directory at runtime.
    # ============================================================================
    nixfleet.files = {
      "/etc/nixfleet/netboot-server.sh" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          ARTIFACTS_DIR="${cfg.artifactsPath}/${cfg.target}"
          KERNEL="$ARTIFACTS_DIR/bzImage"
          INITRD="$ARTIFACTS_DIR/initrd"
          CMDLINE_FILE="$ARTIFACTS_DIR/cmdline"

          # Validate artifacts exist
          for f in "$KERNEL" "$INITRD" "$CMDLINE_FILE"; do
            if [ ! -f "$f" ]; then
              echo "ERROR: Missing artifact: $f" >&2
              echo "Run 'netboot-update ${cfg.target} /path/to/result' to populate artifacts" >&2
              exit 1
            fi
          done

          CMDLINE=$(cat "$CMDLINE_FILE")

          echo "Starting pixiecore netboot server"
          echo "  Target:  ${cfg.target}"
          echo "  Kernel:  $KERNEL"
          echo "  Initrd:  $INITRD"
          echo "  Cmdline: $CMDLINE"
          echo "  HTTP:    port ${toString cfg.httpPort}"
          ${lib.optionalString cfg.dhcpNoBind ''echo "  Mode:    DHCP proxy (no-bind)"''}

          exec pixiecore boot "$KERNEL" "$INITRD" \
            --cmdline "$CMDLINE" \
            --port ${toString cfg.httpPort} \
            ${lib.optionalString cfg.dhcpNoBind "--dhcp-no-bind"} \
            ${lib.optionalString cfg.debug "--debug"} \
            --log-timestamps
        '';
        restartUnits = [ "nixfleet-netboot-server.service" ];
      };

      # ============================================================================
      # Artifact update helper script
      # Usage: netboot-update <target> <result-path>
      # Copies build artifacts from a nix build result into the artifacts directory.
      # ============================================================================
      "/usr/local/bin/netboot-update" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          usage() {
            echo "Usage: netboot-update <target> <result-path>"
            echo ""
            echo "Copy netboot build artifacts into the serving directory."
            echo ""
            echo "Arguments:"
            echo "  target       Target name (e.g., gtr)"
            echo "  result-path  Path to nix build result (containing bzImage, initrd, cmdline)"
            echo ""
            echo "Example:"
            echo "  nix build .#netboot-gtr"
            echo "  netboot-update gtr ./result"
            exit 1
          }

          if [ $# -ne 2 ]; then
            usage
          fi

          TARGET="$1"
          RESULT="$2"
          DEST="${cfg.artifactsPath}/$TARGET"

          if [ ! -d "$RESULT" ]; then
            echo "ERROR: Result path '$RESULT' does not exist or is not a directory" >&2
            exit 1
          fi

          # Validate expected files
          for f in bzImage initrd cmdline; do
            if [ ! -e "$RESULT/$f" ]; then
              echo "ERROR: Expected file '$f' not found in $RESULT" >&2
              exit 1
            fi
          done

          echo "Updating netboot artifacts for target '$TARGET'"
          mkdir -p "$DEST"

          # Copy artifacts (dereference symlinks from nix store)
          cp -L "$RESULT/bzImage" "$DEST/bzImage"
          cp -L "$RESULT/initrd" "$DEST/initrd"
          cp -L "$RESULT/cmdline" "$DEST/cmdline"

          # Copy squashfs if present (used for reference/debugging)
          if [ -e "$RESULT/nix-store.squashfs" ]; then
            cp -L "$RESULT/nix-store.squashfs" "$DEST/nix-store.squashfs"
          fi

          echo "Artifacts updated in $DEST:"
          ls -lh "$DEST/"
          echo ""
          echo "Restart the netboot server to pick up new artifacts:"
          echo "  systemctl restart nixfleet-netboot-server"
        '';
      };
    };

    # ============================================================================
    # Systemd service
    # ============================================================================
    nixfleet.systemd.units."nixfleet-netboot-server.service" = {
      text = ''
        [Unit]
        Description=NixFleet PXE Netboot Server (pixiecore)
        After=network-online.target
        Wants=network-online.target

        [Service]
        Type=simple
        ExecStart=/etc/nixfleet/netboot-server.sh
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
    # Health check
    # ============================================================================
    nixfleet.healthChecks.netboot-server = {
      type = "command";
      command = "systemctl is-active nixfleet-netboot-server";
      timeout = 5;
    };
  };
}

#!/bin/bash
#
# NixFleet Installer Setup
#
# Downloads Ubuntu 24.04 Server ISO and extracts kernel + initrd
# for PXE booting via pixiecore.
#
# Usage:
#   installer-setup [--iso-url URL] [--dest DIR]

set -euo pipefail

UBUNTU_VERSION="24.04.2"
ISO_URL="${ISO_URL:-https://releases.ubuntu.com/${UBUNTU_VERSION}/ubuntu-${UBUNTU_VERSION}-live-server-amd64.iso}"
DEST="${DEST:-/srv/installer/boot}"
ISO_CACHE="/var/cache/nixfleet"

log() { echo "[installer-setup] $*"; }

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --iso-url) ISO_URL="$2"; shift 2 ;;
    --dest)    DEST="$2"; shift 2 ;;
    --help|-h)
      echo "Usage: installer-setup [--iso-url URL] [--dest DIR]"
      echo ""
      echo "Downloads Ubuntu server ISO and extracts PXE boot files."
      echo ""
      echo "Options:"
      echo "  --iso-url URL   Ubuntu ISO URL (default: Ubuntu ${UBUNTU_VERSION})"
      echo "  --dest DIR      Destination for boot files (default: /srv/installer/boot)"
      exit 0
      ;;
    *) log "Unknown option: $1"; exit 1 ;;
  esac
done

# Check for root
if [[ $EUID -ne 0 ]]; then
  log "ERROR: This script must be run as root (need to mount ISO)"
  exit 1
fi

mkdir -p "$DEST" "$ISO_CACHE"

ISO_FILE="$ISO_CACHE/ubuntu-${UBUNTU_VERSION}-live-server-amd64.iso"

# ============================================================================
# Step 1: Download ISO (if not cached)
# ============================================================================
if [[ -f "$ISO_FILE" ]]; then
  log "Using cached ISO: $ISO_FILE"
else
  log "Downloading Ubuntu ${UBUNTU_VERSION} server ISO..."
  log "  URL: $ISO_URL"
  curl -L --progress-bar -o "$ISO_FILE.tmp" "$ISO_URL"
  mv "$ISO_FILE.tmp" "$ISO_FILE"
  log "Downloaded: $ISO_FILE"
fi

# ============================================================================
# Step 2: Mount ISO and extract boot files
# ============================================================================
MOUNT_DIR=$(mktemp -d)
trap 'umount "$MOUNT_DIR" 2>/dev/null; rmdir "$MOUNT_DIR" 2>/dev/null' EXIT

log "Mounting ISO..."
mount -o loop,ro "$ISO_FILE" "$MOUNT_DIR"

# Extract kernel and initrd
log "Extracting boot files..."
cp "$MOUNT_DIR/casper/vmlinuz" "$DEST/vmlinuz"
cp "$MOUNT_DIR/casper/initrd" "$DEST/initrd"

# Also copy the squashfs filesystem (needed for live boot / recovery)
if [[ -f "$MOUNT_DIR/casper/ubuntu-server-minimal.squashfs" ]]; then
  log "Copying minimal squashfs (for recovery mode)..."
  cp "$MOUNT_DIR/casper/ubuntu-server-minimal.squashfs" "$DEST/filesystem.squashfs"
elif [[ -f "$MOUNT_DIR/casper/filesystem.squashfs" ]]; then
  log "Copying squashfs (for recovery mode)..."
  cp "$MOUNT_DIR/casper/filesystem.squashfs" "$DEST/filesystem.squashfs"
fi

umount "$MOUNT_DIR"

# ============================================================================
# Step 3: Verify
# ============================================================================
log "Boot files installed:"
ls -lh "$DEST/"

log ""
log "Setup complete. Boot files are at: $DEST"
log ""
log "Next steps:"
log "  1. Start the PXE server:  systemctl start nixfleet-installer-pxe"
log "  2. Add install target:    installer-pxe add-install <mac> <hostname>"
log "  3. Add recovery target:   installer-pxe add-recovery <mac>"
log "  4. PXE boot the machine"

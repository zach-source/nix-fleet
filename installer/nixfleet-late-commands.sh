#!/bin/bash
#
# NixFleet Late Commands Bootstrap Script
#
# Runs during Ubuntu autoinstall late-commands phase (inside curtin in-target chroot).
# Bootstraps the host for NixFleet management:
#   1. Install Nix multi-user daemon
#   2. Create deploy user with SSH + sudoers
#   3. Create NixFleet directories + state.json
#   4. Install TPM2 initramfs hooks
#   5. Install nixfleet-firstboot.service (TPM2 enroll + apply)
#
# Usage:
#   bash nixfleet-late-commands.sh \
#     --deploy-user deploy \
#     --ssh-key "ssh-ed25519 ..." \
#     --hostname myhostname \
#     --luks-passphrase "nixfleet-init-myhostname" \
#     --mgmt-host "192.168.3.131"

set -euo pipefail

# Defaults
DEPLOY_USER="deploy"
SSH_KEY=""
HOSTNAME=""
LUKS_PASSPHRASE=""
MGMT_HOST=""

log() { echo "[nixfleet-bootstrap] $*"; }

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --deploy-user) DEPLOY_USER="$2"; shift 2 ;;
    --ssh-key)     SSH_KEY="$2"; shift 2 ;;
    --hostname)    HOSTNAME="$2"; shift 2 ;;
    --luks-passphrase) LUKS_PASSPHRASE="$2"; shift 2 ;;
    --mgmt-host)   MGMT_HOST="$2"; shift 2 ;;
    *) log "Unknown option: $1"; exit 1 ;;
  esac
done

: "${HOSTNAME:?--hostname is required}"
: "${SSH_KEY:?--ssh-key is required}"
: "${LUKS_PASSPHRASE:?--luks-passphrase is required}"

# ============================================================================
# Step 1: Install Nix multi-user daemon
# ============================================================================
log "Installing Nix (multi-user mode)..."

apt-get update -qq
apt-get install -y -qq curl xz-utils sudo openssh-server tpm2-tools

# Install Nix — the installer needs /nix to exist
mkdir -p /nix
sh <(curl -L https://nixos.org/nix/install) --daemon --yes

# Source nix for this session
if [ -f /etc/profile.d/nix.sh ]; then
  # shellcheck source=/dev/null
  . /etc/profile.d/nix.sh
fi

# ============================================================================
# Step 2: Create deploy user
# ============================================================================
log "Creating deploy user: $DEPLOY_USER"

if ! id "$DEPLOY_USER" &>/dev/null; then
  useradd --system --create-home --shell /bin/bash "$DEPLOY_USER"
fi

# Add to nix groups
usermod -aG nixbld "$DEPLOY_USER" 2>/dev/null || true

# SSH access
DEPLOY_HOME=$(getent passwd "$DEPLOY_USER" | cut -d: -f6)
SSH_DIR="$DEPLOY_HOME/.ssh"
mkdir -p "$SSH_DIR"
echo "$SSH_KEY" >> "$SSH_DIR/authorized_keys"
sort -u "$SSH_DIR/authorized_keys" -o "$SSH_DIR/authorized_keys"
chmod 700 "$SSH_DIR"
chmod 600 "$SSH_DIR/authorized_keys"
chown -R "$DEPLOY_USER:$DEPLOY_USER" "$SSH_DIR"

# ============================================================================
# Step 3: Configure sudoers
# ============================================================================
log "Configuring sudoers for $DEPLOY_USER"

cat > "/etc/sudoers.d/nixfleet-$DEPLOY_USER" <<SUDOERS
# NixFleet sudo rules for $DEPLOY_USER
$DEPLOY_USER ALL=(ALL) NOPASSWD: /nix/var/nix/profiles/nixfleet/*/bin/*
$DEPLOY_USER ALL=(ALL) NOPASSWD: /run/current-system/sw/bin/nix*
$DEPLOY_USER ALL=(ALL) NOPASSWD: /nix/var/nix/profiles/default/bin/nix*
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl daemon-reload
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl start *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl stop *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl enable *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl disable *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl status *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/tee /etc/*
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/install -m * -o * -g * *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/sbin/useradd *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/sbin/usermod *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/sbin/groupadd *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/chown *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/chmod *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /usr/bin/mkdir *
$DEPLOY_USER ALL=(ALL) NOPASSWD: /sbin/reboot
SUDOERS

chmod 440 "/etc/sudoers.d/nixfleet-$DEPLOY_USER"
visudo -cf "/etc/sudoers.d/nixfleet-$DEPLOY_USER"

# ============================================================================
# Step 4: Create NixFleet directories + state.json
# ============================================================================
log "Creating NixFleet directories..."

mkdir -p /nix/var/nix/profiles/nixfleet
mkdir -p /var/lib/nixfleet
mkdir -p /etc/.nixfleet/staging
mkdir -p /run/nixfleet-secrets
chmod 750 /var/lib/nixfleet
chmod 700 /etc/.nixfleet/staging
chmod 700 /run/nixfleet-secrets

cat > /var/lib/nixfleet/state.json <<STATE
{
  "bootstrapped": "$(date -Iseconds)",
  "generation": 0,
  "manifestHash": null,
  "lastApply": null,
  "osUpdate": {
    "lastRun": null,
    "lastPackages": []
  },
  "rebootNeeded": false
}
STATE

# ============================================================================
# Step 5: Install TPM2 initramfs hooks
# ============================================================================
log "Installing TPM2 initramfs hooks..."

# initramfs hook: load TPM2 modules early
mkdir -p /etc/initramfs-tools/hooks
cat > /etc/initramfs-tools/hooks/tpm2-unlock <<'HOOK'
#!/bin/sh
PREREQ=""
prereqs() { echo "$PREREQ"; }
case $1 in prereqs) prereqs; exit 0;; esac
. /usr/share/initramfs-tools/hook-functions

# Copy TPM2 userspace tools
copy_exec /usr/bin/systemd-cryptenroll /usr/bin
copy_exec /usr/lib/x86_64-linux-gnu/cryptsetup/libcryptsetup-token-systemd-tpm2.so /usr/lib/x86_64-linux-gnu/cryptsetup/ 2>/dev/null || true

# Copy TPM2 device drivers
manual_add_modules tpm_crb tpm_tis
HOOK
chmod 755 /etc/initramfs-tools/hooks/tpm2-unlock

# initramfs script: open LUKS keystore with TPM2
cat > /etc/initramfs-tools/scripts/local-top/keystore-unlock <<'SCRIPT'
#!/bin/sh
PREREQ="udev"
prereqs() { echo "$PREREQ"; }
case $1 in prereqs) prereqs; exit 0;; esac

KEYSTORE="/dev/zvol/rpool/keystore"

# Wait for ZFS zvol
for i in $(seq 1 30); do
  [ -b "$KEYSTORE" ] && break
  sleep 1
done

if [ ! -b "$KEYSTORE" ]; then
  echo "WARNING: keystore zvol not found, skipping TPM2 unlock"
  exit 0
fi

# Try TPM2 first, fall back to passphrase prompt
if cryptsetup open --type luks2 --token-only "$KEYSTORE" keystore-rpool 2>/dev/null; then
  echo "Keystore unlocked via TPM2"
else
  echo "TPM2 unlock failed, requesting passphrase..."
  cryptsetup open --type luks2 "$KEYSTORE" keystore-rpool
fi
SCRIPT
chmod 755 /etc/initramfs-tools/scripts/local-top/keystore-unlock

# ============================================================================
# Step 6: Install nixfleet-firstboot.service
# ============================================================================
log "Installing firstboot service..."

# Store the initial LUKS passphrase for firstboot TPM2 enrollment
echo -n "$LUKS_PASSPHRASE" > /var/lib/nixfleet/.luks-init-passphrase
chmod 600 /var/lib/nixfleet/.luks-init-passphrase

# Firstboot script — TPM2 enrollment + initial apply
cat > /usr/local/bin/nixfleet-firstboot.sh <<'FIRSTBOOT'
#!/bin/bash
set -euo pipefail

log() { echo "[nixfleet-firstboot] $*"; }

KEYSTORE="/dev/zvol/rpool/keystore"
PASSPHRASE_FILE="/var/lib/nixfleet/.luks-init-passphrase"

if [ ! -f "$PASSPHRASE_FILE" ]; then
  log "No initial passphrase found — firstboot already completed"
  exit 0
fi

INIT_PASSPHRASE=$(cat "$PASSPHRASE_FILE")

# Wait for TPM device
log "Waiting for TPM2 device..."
for i in $(seq 1 60); do
  [ -c /dev/tpmrm0 ] && break
  sleep 1
done

if [ ! -c /dev/tpmrm0 ]; then
  log "ERROR: TPM2 device not found after 60s"
  exit 1
fi

# Wait for keystore zvol
log "Waiting for keystore zvol..."
for i in $(seq 1 30); do
  [ -b "$KEYSTORE" ] && break
  sleep 1
done

if [ ! -b "$KEYSTORE" ]; then
  log "ERROR: Keystore zvol not found"
  exit 1
fi

# Enroll TPM2 with PCR 7 (Secure Boot state)
log "Enrolling TPM2..."
echo -n "$INIT_PASSPHRASE" | systemd-cryptenroll "$KEYSTORE" \
  --tpm2-device=auto \
  --tpm2-pcrs=7 \
  --password-file=/dev/stdin

# Generate and add a random recovery passphrase
log "Adding recovery passphrase..."
RECOVERY_PASS=$(openssl rand -base64 32)
echo -n "$INIT_PASSPHRASE" | systemd-cryptenroll "$KEYSTORE" \
  --password --new-passphrase-file=<(echo -n "$RECOVERY_PASS") \
  --password-file=/dev/stdin

# Store recovery passphrase
mkdir -p /run/nixfleet-secrets
echo "$RECOVERY_PASS" > /run/nixfleet-secrets/luks-recovery-passphrase
chmod 0400 /run/nixfleet-secrets/luks-recovery-passphrase

# Wipe the initial passphrase keyslot (slot 0)
log "Wiping initial passphrase keyslot..."
systemd-cryptenroll "$KEYSTORE" --wipe-slot=0

# Remove the stored initial passphrase
rm -f "$PASSPHRASE_FILE"

# Update initramfs with TPM2 hooks
log "Updating initramfs..."
update-initramfs -u -k all

log "TPM2 enrollment complete"
log "  - TPM2 auto-unlock: enabled (PCR 7)"
log "  - Recovery passphrase: /run/nixfleet-secrets/luks-recovery-passphrase"
log "  - Initial passphrase: wiped"

# Disable this service so it doesn't run again
systemctl disable nixfleet-firstboot.service
FIRSTBOOT
chmod 755 /usr/local/bin/nixfleet-firstboot.sh

# Systemd service for firstboot
cat > /etc/systemd/system/nixfleet-firstboot.service <<'SERVICE'
[Unit]
Description=NixFleet First Boot (TPM2 Enrollment)
After=local-fs.target systemd-udevd.service
ConditionPathExists=/var/lib/nixfleet/.luks-init-passphrase

[Service]
Type=oneshot
ExecStart=/usr/local/bin/nixfleet-firstboot.sh
RemainAfterExit=true
StandardOutput=journal+console

[Install]
WantedBy=multi-user.target
SERVICE

systemctl enable nixfleet-firstboot.service

# ============================================================================
# Step 7: Update initramfs
# ============================================================================
log "Building initramfs with TPM2 hooks..."
update-initramfs -u -k all

# ============================================================================
# Done
# ============================================================================
log "NixFleet bootstrap complete"
log "  Deploy user: $DEPLOY_USER"
log "  Nix daemon:  $(systemctl is-active nix-daemon 2>/dev/null || echo 'pending')"
log "  Firstboot:   nixfleet-firstboot.service (enabled)"
log "  TPM2 enrollment will run on first reboot"

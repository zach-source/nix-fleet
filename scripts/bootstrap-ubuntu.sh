#!/usr/bin/env bash
#
# NixFleet Ubuntu Bootstrap Script
#
# This script prepares an Ubuntu host for NixFleet management by:
# 1. Installing the Nix package manager (multi-user mode)
# 2. Creating a deploy user with SSH access
# 3. Configuring sudoers for passwordless Nix operations
# 4. Setting up the NixFleet profile directory
#
# Usage:
#   curl -sSL https://your-domain/bootstrap-ubuntu.sh | sudo bash
#   # or
#   sudo ./bootstrap-ubuntu.sh [OPTIONS]
#
# Options:
#   --deploy-user NAME    Username for deployments (default: deploy)
#   --ssh-key "KEY"       SSH public key for deploy user
#   --skip-nix            Skip Nix installation (if already installed)
#   --help                Show this help message

set -euo pipefail

# Configuration
DEPLOY_USER="${DEPLOY_USER:-deploy}"
SSH_KEY="${SSH_KEY:-}"
SKIP_NIX="${SKIP_NIX:-false}"
NIX_INSTALL_URL="https://nixos.org/nix/install"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
        echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
        echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
        echo -e "${RED}[ERROR]${NC} $1"
}

usage() {
        head -30 "$0" | grep '^#' | sed 's/^# \?//'
        exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
        case $1 in
        --deploy-user)
                DEPLOY_USER="$2"
                shift 2
                ;;
        --ssh-key)
                SSH_KEY="$2"
                shift 2
                ;;
        --skip-nix)
                SKIP_NIX=true
                shift
                ;;
        --help | -h)
                usage
                ;;
        *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
done

# Check if running as root
if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root"
        exit 1
fi

# Check OS
if [[ ! -f /etc/os-release ]] || ! grep -q "Ubuntu" /etc/os-release; then
        log_error "This script is designed for Ubuntu systems"
        exit 1
fi

log_info "Starting NixFleet bootstrap on Ubuntu..."

# Step 1: Install Nix (multi-user mode)
if [[ "$SKIP_NIX" == "true" ]]; then
        log_info "Skipping Nix installation (--skip-nix)"
elif command -v nix &>/dev/null; then
        log_info "Nix is already installed"
        nix --version
else
        log_info "Installing Nix (multi-user mode)..."

        # Install prerequisites
        apt-get update
        apt-get install -y curl xz-utils sudo

        # Install Nix
        sh <(curl -L "$NIX_INSTALL_URL") --daemon --yes

        # Source nix profile for current session
        if [[ -f /etc/profile.d/nix.sh ]]; then
                # shellcheck source=/dev/null
                source /etc/profile.d/nix.sh
        fi

        log_info "Nix installed successfully"
fi

# Verify Nix daemon is running
if ! systemctl is-active --quiet nix-daemon; then
        log_warn "Nix daemon is not running, starting it..."
        systemctl enable --now nix-daemon
fi

# Step 2: Create deploy user
if id "$DEPLOY_USER" &>/dev/null; then
        log_info "User $DEPLOY_USER already exists"
else
        log_info "Creating deploy user: $DEPLOY_USER"
        useradd --system --create-home --shell /bin/bash "$DEPLOY_USER"
fi

# Add deploy user to nix groups
usermod -aG nixbld "$DEPLOY_USER" 2>/dev/null || true

# Step 3: Configure SSH access
DEPLOY_HOME=$(getent passwd "$DEPLOY_USER" | cut -d: -f6)
SSH_DIR="$DEPLOY_HOME/.ssh"

mkdir -p "$SSH_DIR"
chmod 700 "$SSH_DIR"

if [[ -n "$SSH_KEY" ]]; then
        log_info "Adding SSH key for $DEPLOY_USER"
        echo "$SSH_KEY" >>"$SSH_DIR/authorized_keys"
        sort -u "$SSH_DIR/authorized_keys" -o "$SSH_DIR/authorized_keys"
        chmod 600 "$SSH_DIR/authorized_keys"
else
        log_warn "No SSH key provided. Add one manually to $SSH_DIR/authorized_keys"
fi

chown -R "$DEPLOY_USER:$DEPLOY_USER" "$SSH_DIR"

# Step 4: Configure sudoers for passwordless Nix operations
SUDOERS_FILE="/etc/sudoers.d/nixfleet-$DEPLOY_USER"

log_info "Configuring sudoers for $DEPLOY_USER"
cat >"$SUDOERS_FILE" <<EOF
# NixFleet sudo rules for $DEPLOY_USER
# Allows passwordless execution of Nix and systemctl commands

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
EOF

chmod 440 "$SUDOERS_FILE"
visudo -cf "$SUDOERS_FILE" || {
        log_error "Invalid sudoers file generated"
        rm -f "$SUDOERS_FILE"
        exit 1
}

# Step 5: Create NixFleet directories
log_info "Creating NixFleet directories..."
mkdir -p /nix/var/nix/profiles/nixfleet
mkdir -p /var/lib/nixfleet
mkdir -p /etc/.nixfleet/staging
chmod 750 /var/lib/nixfleet
chmod 700 /etc/.nixfleet/staging

# Initialize state file
if [[ ! -f /var/lib/nixfleet/state.json ]]; then
        cat >/var/lib/nixfleet/state.json <<EOF
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
EOF
fi

# Step 6: Verify installation
log_info "Verifying installation..."

echo ""
echo "=============================================="
echo "NixFleet Bootstrap Complete!"
echo "=============================================="
echo ""
echo "Deploy user:     $DEPLOY_USER"
echo "Nix version:     $(nix --version 2>/dev/null || echo 'not in path')"
echo "Nix daemon:      $(systemctl is-active nix-daemon)"
echo "Profile path:    /nix/var/nix/profiles/nixfleet"
echo "State file:      /var/lib/nixfleet/state.json"
echo ""

if [[ -z "$SSH_KEY" ]]; then
        echo "IMPORTANT: Add an SSH public key to $SSH_DIR/authorized_keys"
        echo ""
fi

echo "Next steps:"
echo "  1. Add this host to your NixFleet inventory"
echo "  2. Run 'nixfleet plan --host $(hostname)' to preview changes"
echo "  3. Run 'nixfleet apply --host $(hostname)' to deploy"
echo ""

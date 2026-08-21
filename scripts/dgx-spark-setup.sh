#!/usr/bin/env bash
#
# NixFleet DGX Spark Setup
#
# Runs ON a DGX Spark, as root. Does the three things that need a password and
# therefore cannot be driven from the workstation:
#
#   1. Creates `nixbot` — the fleet escape-hatch user (passwordless sudo)
#   2. Runs bootstrap-ubuntu.sh — installs Nix, creates `deploy`, sudoers, dirs
#   3. Seeds `trusted-users` so `nixfleet apply` can copy unsigned closures
#
# Step 3 is a genuine chicken-and-egg: modules/nix-config.nix owns
# trusted-users, but it can only arrive via an apply that the missing trust
# blocks. Seed once here, and the module keeps it consistent thereafter.
#
# Idempotent — safe to re-run.
#
# Usage:
#   scp scripts/{bootstrap-ubuntu.sh,dgx-spark-setup.sh} <you>@<spark>:/tmp/
#   ssh -t <you>@<spark> 'sudo bash /tmp/dgx-spark-setup.sh'
#
# Options:
#   --deploy-key "KEY"   Public key for the deploy user (default: baked in)
#   --skip-nix           Skip Nix installation
#   --help               Show this help
#
# Deliberately does NOT touch OS updates. Those belong to DGX Dashboard and
# fwupdmgr — see docs/dgx-spark-onboarding.md.

set -euo pipefail

# Fleet public keys. Public by definition — safe to keep in the repo.
NIXBOT_KEYS=(
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAt4RzLTsBILE4lf8vdzSGQpFoxDznF/ieCOyddXz2+4 GitHub"
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIF0xNAQcC206JS3rcSwsZccWHGJoq976hSOMOqoyNSm agent-claude-workspace-ztaylor@localhost"
)
DEPLOY_KEY="${DEPLOY_KEY:-ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIx8O0qSASADun8EpYDmUK7OLoS3CDMgsqmPQkSydJwz nixfleet-deploy@ztaylor}"

# uid 30033 matches gti, the only other host with nixbot. Keeping it uniform
# means NFS and any uid-mapped path behave the same across the fleet.
NIXBOT_UID=30033
SKIP_NIX=false
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

usage() {
	head -30 "$0" | grep '^#' | sed 's/^# \?//'
	exit 0
}

while [[ $# -gt 0 ]]; do
	case $1 in
	--deploy-key)
		DEPLOY_KEY="$2"
		shift 2
		;;
	--skip-nix)
		SKIP_NIX=true
		shift
		;;
	--help | -h) usage ;;
	*)
		log_error "Unknown option: $1"
		exit 1
		;;
	esac
done

if [[ $EUID -ne 0 ]]; then
	log_error "Must run as root (use: sudo bash $0)"
	exit 1
fi

# Confirm this really is a Spark. DGX OS reports ID=ubuntu, so os-release alone
# cannot tell you — /etc/dgx-release is what distinguishes it.
if [[ ! -f /etc/dgx-release ]]; then
	log_error "/etc/dgx-release not found — this does not look like a DGX host."
	log_error "Use scripts/bootstrap-ubuntu.sh directly for a plain Ubuntu host."
	exit 1
fi

# shellcheck source=/dev/null
DGX_NAME=$(grep '^DGX_PRETTY_NAME=' /etc/dgx-release | cut -d'"' -f2)
DGX_VERSION=$(grep '^DGX_SWBUILD_VERSION=' /etc/dgx-release | cut -d'"' -f2)
log_info "Host: $(hostname) — ${DGX_NAME} ${DGX_VERSION} ($(uname -m))"

# ============================================================================
# Step 1: nixbot — fleet escape hatch
# ============================================================================
log_info "Step 1: nixbot"

if id nixbot &>/dev/null; then
	log_info "  User already exists (uid $(id -u nixbot))"
elif getent passwd "$NIXBOT_UID" &>/dev/null; then
	# Don't silently land nixbot on a different uid than the rest of the fleet.
	log_error "  uid $NIXBOT_UID is taken by $(getent passwd $NIXBOT_UID | cut -d: -f1)"
	log_error "  Resolve by hand — a fleet-inconsistent uid is worse than stopping here."
	exit 1
else
	useradd -m -u "$NIXBOT_UID" -s /bin/bash -G sudo nixbot
	log_info "  Created nixbot (uid $NIXBOT_UID, group sudo)"
fi

install -d -m 700 -o nixbot -g nixbot /home/nixbot/.ssh
printf '%s\n' "${NIXBOT_KEYS[@]}" >/home/nixbot/.ssh/authorized_keys
chown nixbot:nixbot /home/nixbot/.ssh/authorized_keys
chmod 600 /home/nixbot/.ssh/authorized_keys
log_info "  Installed ${#NIXBOT_KEYS[@]} authorized key(s)"

# Full passwordless root, matching gti. Stated plainly rather than inherited.
echo 'nixbot ALL=(ALL) NOPASSWD: ALL' >/etc/sudoers.d/nixbot
chmod 440 /etc/sudoers.d/nixbot
visudo -cf /etc/sudoers.d/nixbot >/dev/null || {
	log_error "  Invalid sudoers file — removing"
	rm -f /etc/sudoers.d/nixbot
	exit 1
}
log_info "  Passwordless sudo configured"

# ============================================================================
# Step 2: Nix + deploy user (delegated to the existing bootstrap)
# ============================================================================
log_info "Step 2: Nix and deploy user"

BOOTSTRAP="${SCRIPT_DIR}/bootstrap-ubuntu.sh"
if [[ ! -f "$BOOTSTRAP" ]]; then
	log_error "  bootstrap-ubuntu.sh not found next to this script."
	log_error "  Copy both:  scp scripts/{bootstrap-ubuntu.sh,dgx-spark-setup.sh} <you>@<host>:/tmp/"
	exit 1
fi

BOOTSTRAP_ARGS=(--ssh-key "$DEPLOY_KEY")
[[ "$SKIP_NIX" == "true" ]] && BOOTSTRAP_ARGS+=(--skip-nix)

bash "$BOOTSTRAP" "${BOOTSTRAP_ARGS[@]}"

# ============================================================================
# Step 3: trusted-users seed
# ============================================================================
log_info "Step 3: trusted-users seed"

NIX_CUSTOM=/etc/nix/nix.custom.conf
mkdir -p /etc/nix
touch "$NIX_CUSTOM"

if grep -qE '^\s*extra-trusted-users\s*=.*\bdeploy\b' "$NIX_CUSTOM"; then
	log_info "  Already present"
else
	echo "extra-trusted-users = deploy" >>"$NIX_CUSTOM"
	log_info "  Appended to $NIX_CUSTOM"
fi

# Determinate Nix's /etc/nix/nix.conf !includes nix.custom.conf. A stock
# installer writes no such line, so the seed would be read by nobody.
if [[ -f /etc/nix/nix.conf ]] && ! grep -q 'nix.custom.conf' /etc/nix/nix.conf; then
	echo '!include nix.custom.conf' >>/etc/nix/nix.conf
	log_info "  Added !include to /etc/nix/nix.conf"
fi

if systemctl is-active --quiet nix-daemon; then
	systemctl restart nix-daemon
	log_info "  Restarted nix-daemon"
fi

# ============================================================================
# Verify
# ============================================================================
echo ""
echo "=============================================="
echo "DGX Spark setup complete — $(hostname)"
echo "=============================================="
printf "  nixbot:         uid %s, sudo %s\n" \
	"$(id -u nixbot)" "$(runuser -u nixbot -- sudo -n true 2>/dev/null && echo ok || echo FAILED)"
printf "  deploy:         uid %s\n" "$(id -u deploy 2>/dev/null || echo MISSING)"
printf "  nix:            %s\n" "$(command -v nix >/dev/null && nix --version || echo MISSING)"
printf "  trusted-users:  %s\n" "$(grep -h 'trusted-users' "$NIX_CUSTOM" 2>/dev/null | tr -d '\n' || echo MISSING)"
printf "  mgmt iface:     %s\n" "$(ip route get 192.168.3.1 2>/dev/null | grep -o 'dev [^ ]*' | head -1 | cut -d' ' -f2)"
printf "  CX7 cabled:     %s\n" "$(ibdev2netdev 2>/dev/null | grep '(Up)' | awk '{print $5}' | tr '\n' ' ' || echo 'ibdev2netdev missing')"
echo ""
echo "Next, from the workstation:"
echo "  ssh -o KexAlgorithms=curve25519-sha256 nixbot@\$(hostname -I | awk '{print \$1}') 'sudo -n id'"
echo ""

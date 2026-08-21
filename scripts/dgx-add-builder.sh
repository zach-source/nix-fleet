#!/usr/bin/env bash
#
# Register the DGX Sparks as aarch64-linux remote builders
#
# Runs on the WORKSTATION (macOS), as root. Needed because nothing else in the
# fleet can build for a Spark: gti and the gtr nodes are x86_64, and a Mac is
# aarch64-darwin — the right CPU but the wrong kernel. So each Spark builds for
# itself.
#
# Only ~9 derivations actually need this (the NixFleet-generated activation
# script, unit files and netplan configs). The other ~141 paths substitute
# straight from cache.nixos.org, which already has aarch64-linux binaries.
#
# Root is required for a non-obvious reason: the nix DAEMON performs remote
# builds, and it runs as root. So it is ROOT's ~/.ssh/known_hosts that must
# trust the Sparks, not yours. A missing entry surfaces as the thoroughly
# unhelpful "failed to start SSH master connection".
#
# Idempotent — safe to re-run.
#
# Usage:
#   sudo bash scripts/dgx-add-builder.sh
#
# Options:
#   --user NAME    Remote build user on the Sparks (default: deploy)
#   --key PATH     SSH private key (default: <your-home>/.ssh/nixfleet)
#   --help         Show this help

set -euo pipefail

SPARKS=(192.168.3.140 192.168.3.141)
BUILD_USER="deploy"
# 20 cores on a GB10, but these are tiny derivations — 8 is plenty and leaves
# the box responsive if a build lands while it is serving a model.
MAX_JOBS=8
SPEED_FACTOR=1
MACHINES=/etc/nix/machines
NIX_CUSTOM=/etc/nix/nix.custom.conf

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'
log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

usage() {
	head -27 "$0" | grep '^#' | sed 's/^# \?//'
	exit 0
}

SSH_KEY=""
while [[ $# -gt 0 ]]; do
	case $1 in
	--user)
		BUILD_USER="$2"
		shift 2
		;;
	--key)
		SSH_KEY="$2"
		shift 2
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

# The key lives in the invoking user's home, matching how the existing gti
# builder is configured. Root can read it regardless of the 0600 mode.
REAL_USER="${SUDO_USER:-root}"
REAL_HOME=$(eval echo "~${REAL_USER}")
SSH_KEY="${SSH_KEY:-${REAL_HOME}/.ssh/nixfleet}"

if [[ ! -f "$SSH_KEY" ]]; then
	log_error "SSH key not found: $SSH_KEY"
	exit 1
fi
log_info "Build user: ${BUILD_USER}   key: ${SSH_KEY}"

ROOT_SSH="$(eval echo ~root)/.ssh"
install -d -m 700 "$ROOT_SSH"
KNOWN_HOSTS="${ROOT_SSH}/known_hosts"
touch "$KNOWN_HOSTS"
chmod 600 "$KNOWN_HOSTS"

# ============================================================================
# Step 1: teach root to trust the Sparks
# ============================================================================
log_info "Step 1: root known_hosts"

for host in "${SPARKS[@]}"; do
	if ssh-keygen -F "$host" -f "$KNOWN_HOSTS" >/dev/null 2>&1; then
		log_info "  ${host} already known"
	else
		if ssh-keyscan -T 10 "$host" 2>/dev/null >>"$KNOWN_HOSTS"; then
			log_info "  ${host} host keys added"
		else
			log_error "  ${host} unreachable — cannot scan host keys"
			exit 1
		fi
	fi
done

# ============================================================================
# Step 2: register the builders
# ============================================================================
log_info "Step 2: ${MACHINES}"

touch "$MACHINES"
for host in "${SPARKS[@]}"; do
	uri="ssh-ng://${BUILD_USER}@${host}"
	if grep -qF "$uri " "$MACHINES" 2>/dev/null; then
		log_info "  ${host} already registered"
	else
		printf '%s aarch64-linux %s %s %s big-parallel -\n' \
			"$uri" "$SSH_KEY" "$MAX_JOBS" "$SPEED_FACTOR" >>"$MACHINES"
		log_info "  ${host} registered (maxjobs ${MAX_JOBS})"
	fi
done

# ============================================================================
# Step 3: let builders substitute for themselves
# ============================================================================
log_info "Step 3: builders-use-substitutes"

# Without this the workstation downloads all 161 MiB and pushes it over SSH,
# rather than the Spark pulling from cache.nixos.org directly.
if grep -qE '^\s*builders-use-substitutes\s*=\s*true' "$NIX_CUSTOM" 2>/dev/null; then
	log_info "  Already set"
else
	echo "builders-use-substitutes = true" >>"$NIX_CUSTOM"
	log_info "  Added to ${NIX_CUSTOM}"
fi

# ============================================================================
# Step 4: restart the daemon
# ============================================================================
log_info "Step 4: restart nix-daemon"

# Determinate Nix replaces org.nixos.nix-daemon with its own label, and leaves
# the old one present-but-disabled — so probe rather than assume.
DAEMON_LABEL=""
for label in systems.determinate.nix-daemon org.nixos.nix-daemon; do
	if launchctl print "system/${label}" >/dev/null 2>&1; then
		DAEMON_LABEL="$label"
		break
	fi
done

if [[ -n "$DAEMON_LABEL" ]]; then
	launchctl kickstart -k "system/${DAEMON_LABEL}"
	log_info "  Restarted ${DAEMON_LABEL}"
	# The socket takes a moment to come back; a build started immediately
	# after the kickstart can race it.
	sleep 3
else
	log_warn "  No nix-daemon found — restart it yourself before building"
fi

# ============================================================================
# Verify — as root, which is what actually matters
# ============================================================================
log_info "Verifying root can reach each Spark"

failed=0
for host in "${SPARKS[@]}"; do
	if ssh -q -o BatchMode=yes -o ConnectTimeout=10 -i "$SSH_KEY" \
		-o IdentitiesOnly=yes "${BUILD_USER}@${host}" 'nix --version' >/dev/null 2>&1; then
		log_info "  ${host} OK"
	else
		log_error "  ${host} FAILED"
		failed=1
	fi
done

echo ""
if [[ $failed -eq 0 ]]; then
	echo "=============================================="
	echo "aarch64-linux builders registered"
	echo "=============================================="
	echo ""
	echo "Test it:"
	echo "  nix build --impure --no-link --print-out-paths \\"
	echo "    .#nixfleetConfigurations.dgx-spark-1.system"
else
	log_error "At least one Spark is unreachable as root — builds will fail."
	exit 1
fi

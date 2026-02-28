#!/usr/bin/env bash
set -euo pipefail

OP_ACCOUNT="S43LKCIJPNGYLE52ZXH2MM7LJA"  # stigenai.1password.com
OP_VAULT="Personal Agents"
OP_ITEM="claude-code-oauth"
ACCOUNTS=("" "zctaylor.work@gmail.com" "zachncst@gmail.com" "ztaylor@stigen.ai")
SSH_HOST="gti"
AGENT_NAMESPACES="agent-coder agent-sre agent-pm agent-sage agent-orchestrator agent-personal"

usage() {
  cat <<EOF
Usage: fleet-cc-switch <1|2|3> [--restart]
       fleet-cc-switch --status

Switch which Claude Code Max account token is active across the fleet.

Accounts:
  1  ${ACCOUNTS[1]}
  2  ${ACCOUNTS[2]}
  3  ${ACCOUNTS[3]}

Options:
  --status, -s   Show current active account and stored tokens
  --restart      After switching, restart 1Password operator + agent pods

Examples:
  fleet-cc-switch --status          # Check current state
  fleet-cc-switch 2                 # Switch to account 2 (1Password only)
  fleet-cc-switch 2 --restart       # Switch + sync to K8s + restart pods
EOF
  exit 1
}

cmd_status() {
  local active account_name
  active=$(op item get "$OP_ITEM" --vault "$OP_VAULT" --account="$OP_ACCOUNT" --fields "active_account" --reveal 2>/dev/null || echo "?")
  if [[ "$active" =~ ^[123]$ ]]; then
    account_name="${ACCOUNTS[$active]}"
  else
    account_name="unknown"
  fi
  echo "Active account: $active ($account_name)"
  echo ""
  for i in 1 2 3; do
    local has="no"
    op item get "$OP_ITEM" --vault "$OP_VAULT" --account="$OP_ACCOUNT" --fields "account_${i}_token" --reveal >/dev/null 2>&1 && has="yes"
    echo "  Account $i (${ACCOUNTS[$i]}): token stored=$has"
  done
}

cmd_switch() {
  local target="$1"
  local restart="${2:-}"
  [[ "$target" =~ ^[123]$ ]] || usage

  echo "Switching fleet to account $target (${ACCOUNTS[$target]})..."

  # Read stored token for target account
  local token
  token=$(op item get "$OP_ITEM" --vault "$OP_VAULT" --account="$OP_ACCOUNT" --fields "account_${target}_token" --reveal)
  if [[ -z "$token" ]]; then
    echo "ERROR: No token stored for account $target" >&2
    exit 1
  fi

  # Update active token + account marker
  op item edit "$OP_ITEM" --vault "$OP_VAULT" --account="$OP_ACCOUNT" \
    "CLAUDE_CODE_OAUTH_TOKEN[concealed]=$token" \
    "active_account=$target" >/dev/null

  echo "Updated 1Password item. Active account: $target (${ACCOUNTS[$target]})"

  if [[ "$restart" == "--restart" ]]; then
    echo "Restarting 1Password operator to force sync..."
    ssh "$SSH_HOST" sudo k0s kubectl rollout restart deployment/onepassword-connect-operator -n onepassword
    sleep 10

    echo "Rolling restart of agent pods..."
    for ns in $AGENT_NAMESPACES; do
      echo "  Deleting ${ns}-0..."
      ssh "$SSH_HOST" sudo k0s kubectl delete pod "${ns}-0" -n "$ns" --ignore-not-found 2>/dev/null || true
    done
    echo "Done. Pods will restart with new token."
  else
    echo ""
    echo "Run with --restart to sync to K8s and restart agent pods."
  fi
}

# Parse args
case "${1:-}" in
  --status|-s) cmd_status ;;
  --help|-h) usage ;;
  [123]) cmd_switch "$1" "${2:-}" ;;
  *) usage ;;
esac

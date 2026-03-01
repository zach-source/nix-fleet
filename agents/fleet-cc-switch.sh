#!/usr/bin/env bash
set -euo pipefail

OP_ACCOUNT="S43LKCIJPNGYLE52ZXH2MM7LJA"  # stigenai.1password.com
OP_VAULT="Personal Agents"
OP_ITEM="claude-code-oauth"
ACCOUNTS=("" "zctaylor.work@gmail.com" "zachncst@gmail.com" "ztaylor@stigen.ai")
SSH_HOST="gti"
AGENT_NAMES="agent-coder agent-sre agent-pm agent-sage agent-orchestrator agent-personal"

usage() {
  cat <<EOF
Usage: fleet-cc-switch <command> [args]

Manage per-agent Claude Code account assignments.

Commands:
  status                          Show per-agent account assignments
  assign <agent|all> <1|2|3>      Change agent's Claude Code account
  limits [1|2|3|all]              Check usage limits per account
  rotate <1|2|3>                  Update stored token for an account

Accounts:
  1  ${ACCOUNTS[1]}  (secondary)
  2  ${ACCOUNTS[2]}  (reserve)
  3  ${ACCOUNTS[3]}  (primary)

Examples:
  fleet-cc-switch status
  fleet-cc-switch assign agent-pm 3
  fleet-cc-switch assign all 3
  fleet-cc-switch limits
  fleet-cc-switch limits 3
  fleet-cc-switch rotate 1
EOF
  exit 1
}

cmd_status() {
  printf "%-22s %-10s %s\n" "AGENT" "ACCOUNT" "EMAIL"
  printf "%-22s %-10s %s\n" "-----" "-------" "-----"
  for agent in $AGENT_NAMES; do
    local acct
    acct=$(ssh "$SSH_HOST" "sudo k0s kubectl get statefulset $agent -n $agent -o json" 2>/dev/null \
      | python3 -c "import json,sys;[print(e['value']) for e in json.load(sys.stdin)['spec']['template']['spec']['containers'][0].get('env',[]) if e['name']=='CLAUDE_ACCOUNT']" 2>/dev/null || echo "?")
    local email="unknown"
    if [[ "$acct" =~ ^[123]$ ]]; then
      email="${ACCOUNTS[$acct]}"
    fi
    printf "%-22s %-10s %s\n" "$agent" "$acct" "$email"
  done
}

cmd_assign() {
  local target_agent="$1"
  local acct="$2"
  [[ "$acct" =~ ^[123]$ ]] || { echo "ERROR: Account must be 1, 2, or 3" >&2; exit 1; }

  local agents_to_update
  if [[ "$target_agent" == "all" ]]; then
    agents_to_update="$AGENT_NAMES"
  else
    # Normalize: allow "coder" or "agent-coder"
    [[ "$target_agent" == agent-* ]] || target_agent="agent-$target_agent"
    # Validate agent name
    if ! echo "$AGENT_NAMES" | tr ' ' '\n' | grep -qx "$target_agent"; then
      echo "ERROR: Unknown agent '$target_agent'. Valid: $AGENT_NAMES" >&2
      exit 1
    fi
    agents_to_update="$target_agent"
  fi

  for agent in $agents_to_update; do
    echo "Assigning $agent → account $acct (${ACCOUNTS[$acct]})..."
    ssh "$SSH_HOST" sudo k0s kubectl set env "statefulset/$agent" -n "$agent" "CLAUDE_ACCOUNT=$acct"
  done
  echo "Done. Pods will rolling-restart with new account."
}

cmd_limits() {
  local target="${1:-all}"
  local accounts_to_check

  if [[ "$target" == "all" ]]; then
    accounts_to_check="1 2 3"
  elif [[ "$target" =~ ^[123]$ ]]; then
    accounts_to_check="$target"
  else
    echo "ERROR: Specify account 1, 2, 3, or 'all'" >&2
    exit 1
  fi

  if ! command -v claude &>/dev/null; then
    echo "ERROR: 'claude' CLI not found. Install @anthropic-ai/claude-code." >&2
    exit 1
  fi

  for acct in $accounts_to_check; do
    echo "=== Account $acct (${ACCOUNTS[$acct]}) ==="
    local token
    token=$(op item get "$OP_ITEM" --vault "$OP_VAULT" --account="$OP_ACCOUNT" --fields "account_${acct}_token" --reveal 2>/dev/null)
    if [[ -z "$token" ]]; then
      echo "  No token stored"
      echo ""
      continue
    fi
    CLAUDE_CODE_OAUTH_TOKEN="$token" claude /usage 2>&1 | sed 's/^/  /' || echo "  (failed to query usage)"
    echo ""
  done
}

cmd_rotate() {
  local acct="$1"
  [[ "$acct" =~ ^[123]$ ]] || { echo "ERROR: Account must be 1, 2, or 3" >&2; exit 1; }

  echo "Rotating token for account $acct (${ACCOUNTS[$acct]})"
  echo "Paste the new CLAUDE_CODE_OAUTH_TOKEN value, then press Enter:"
  read -r new_token
  if [[ -z "$new_token" ]]; then
    echo "ERROR: Empty token" >&2
    exit 1
  fi

  op item edit "$OP_ITEM" --vault "$OP_VAULT" --account="$OP_ACCOUNT" \
    "account_${acct}_token[concealed]=$new_token" >/dev/null

  echo "Updated account_${acct}_token in 1Password."
  echo "Restart 1Password operator + agent pods to pick up new token:"
  echo "  ssh gti sudo k0s kubectl rollout restart deployment/onepassword-connect-operator -n onepassword"
}

# Parse args
case "${1:-}" in
  status|-s|--status) cmd_status ;;
  assign) [[ $# -ge 3 ]] || usage; cmd_assign "$2" "$3" ;;
  limits) cmd_limits "${2:-all}" ;;
  rotate) [[ $# -ge 2 ]] || usage; cmd_rotate "$2" ;;
  --help|-h) usage ;;
  *) usage ;;
esac

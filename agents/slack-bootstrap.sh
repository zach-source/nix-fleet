#!/usr/bin/env bash
# slack-bootstrap.sh — Create or update Slack apps for all agents from manifests
#
# Prerequisites:
#   1. Generate a Slack configuration token:
#      https://api.slack.com/apps → profile icon → "Your configuration tokens"
#      → "Generate Token" for your workspace
#   2. Export it: export SLACK_CONFIG_TOKEN="xoxe.xoxp-..."
#
# Usage:
#   ./agents/slack-bootstrap.sh create   # Create new apps for all agents
#   ./agents/slack-bootstrap.sh update   # Update existing apps from manifests
#   ./agents/slack-bootstrap.sh status   # Show current app status
#   ./agents/slack-bootstrap.sh create pm          # Create single agent
#   ./agents/slack-bootstrap.sh update code-review  # Update single agent
#
# After creating apps, you still need to:
#   1. Generate app-level tokens (xapp-) in each app's Basic Information page
#   2. Install each app to the workspace via the OAuth URL
#   3. Store tokens in 1Password for the K8s operator
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_FILE="$SCRIPT_DIR/.slack-apps.json"

# Agent definitions: name → manifest path
declare -A AGENTS=(
  [code-review]="$SCRIPT_DIR/code-review/slack-manifest.yaml"
  [pm]="$SCRIPT_DIR/pm/slack-manifest.yaml"
  [marketing]="$SCRIPT_DIR/marketing/slack-manifest.yaml"
  [devops]="$SCRIPT_DIR/devops/slack-manifest.yaml"
  [research]="$SCRIPT_DIR/research/slack-manifest.yaml"
  [security]="$SCRIPT_DIR/security/slack-manifest.yaml"
)

check_deps() {
  for cmd in yq jq curl; do
    if ! command -v "$cmd" &>/dev/null; then
      echo "Error: $cmd is required but not installed" >&2
      exit 1
    fi
  done
}

check_token() {
  if [[ -z "${SLACK_CONFIG_TOKEN:-}" ]]; then
    echo "Error: SLACK_CONFIG_TOKEN not set" >&2
    echo "" >&2
    echo "Generate one at: https://api.slack.com/apps" >&2
    echo "  → Click your profile icon (top right)" >&2
    echo "  → 'Your configuration tokens'" >&2
    echo "  → 'Generate Token' for your workspace" >&2
    echo "" >&2
    echo "Then: export SLACK_CONFIG_TOKEN=\"xoxe.xoxp-...\"" >&2
    exit 1
  fi
}

slack_api() {
  local method="$1"
  shift
  curl -s -X POST "https://slack.com/api/$method" \
    -H "Authorization: Bearer $SLACK_CONFIG_TOKEN" \
    -H "Content-Type: application/json; charset=utf-8" \
    "$@"
}

load_state() {
  if [[ -f "$STATE_FILE" ]]; then
    cat "$STATE_FILE"
  else
    echo '{}'
  fi
}

save_state() {
  local state="$1"
  echo "$state" | jq '.' > "$STATE_FILE"
}

get_app_id() {
  local agent="$1"
  load_state | jq -r ".\"$agent\".app_id // empty"
}

manifest_to_json() {
  local manifest_path="$1"
  yq -o json "$manifest_path" | jq -c '.'
}

create_app() {
  local agent="$1"
  local manifest_path="${AGENTS[$agent]}"

  if [[ ! -f "$manifest_path" ]]; then
    echo "  Error: manifest not found: $manifest_path" >&2
    return 1
  fi

  local existing_id
  existing_id=$(get_app_id "$agent")
  if [[ -n "$existing_id" ]]; then
    echo "  App already exists: $existing_id (use 'update' to modify)"
    return 0
  fi

  echo "  Creating Slack app from $manifest_path..."
  local manifest_json
  manifest_json=$(manifest_to_json "$manifest_path")

  local response
  response=$(slack_api "apps.manifest.create" -d "{\"manifest\": $manifest_json}")

  local ok
  ok=$(echo "$response" | jq -r '.ok')

  if [[ "$ok" != "true" ]]; then
    local error
    error=$(echo "$response" | jq -r '.error // "unknown"')
    local errors
    errors=$(echo "$response" | jq -r '.errors // [] | .[] | "    - \(.message) (\(.pointer))"')
    echo "  Error creating app: $error" >&2
    if [[ -n "$errors" ]]; then
      echo "$errors" >&2
    fi
    return 1
  fi

  local app_id client_id client_secret signing_secret oauth_url
  app_id=$(echo "$response" | jq -r '.app_id')
  client_id=$(echo "$response" | jq -r '.credentials.client_id')
  client_secret=$(echo "$response" | jq -r '.credentials.client_secret')
  signing_secret=$(echo "$response" | jq -r '.credentials.signing_secret')
  oauth_url=$(echo "$response" | jq -r '.oauth_authorize_url')

  # Save state
  local state
  state=$(load_state)
  state=$(echo "$state" | jq \
    --arg agent "$agent" \
    --arg app_id "$app_id" \
    --arg client_id "$client_id" \
    --arg client_secret "$client_secret" \
    --arg signing_secret "$signing_secret" \
    --arg oauth_url "$oauth_url" \
    --arg created "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '.[$agent] = {app_id: $app_id, client_id: $client_id, client_secret: $client_secret, signing_secret: $signing_secret, oauth_url: $oauth_url, created: $created}')
  save_state "$state"

  echo "  Created: $app_id"
  echo "  OAuth URL: $oauth_url"
  echo ""
  echo "  Next steps for agent-$agent:"
  echo "    1. Visit: https://api.slack.com/apps/$app_id/general"
  echo "       → Generate App-Level Token with 'connections:write' scope"
  echo "    2. Install: $oauth_url"
  echo "    3. Store tokens in 1Password:"
  echo "       - SLACK_BOT_TOKEN (xoxb-... from OAuth install)"
  echo "       - SLACK_APP_TOKEN (xapp-... from app-level token)"
}

update_app() {
  local agent="$1"
  local manifest_path="${AGENTS[$agent]}"

  if [[ ! -f "$manifest_path" ]]; then
    echo "  Error: manifest not found: $manifest_path" >&2
    return 1
  fi

  local app_id
  app_id=$(get_app_id "$agent")
  if [[ -z "$app_id" ]]; then
    echo "  No app registered for $agent. Use 'create' first, or 'link' to register an existing app." >&2
    echo "  To link: $0 link $agent <APP_ID>" >&2
    return 1
  fi

  echo "  Updating $app_id from $manifest_path..."
  local manifest_json
  manifest_json=$(manifest_to_json "$manifest_path")

  local response
  response=$(slack_api "apps.manifest.update" -d "{\"app_id\": \"$app_id\", \"manifest\": $manifest_json}")

  local ok
  ok=$(echo "$response" | jq -r '.ok')

  if [[ "$ok" != "true" ]]; then
    local error
    error=$(echo "$response" | jq -r '.error // "unknown"')
    local errors
    errors=$(echo "$response" | jq -r '.errors // [] | .[] | "    - \(.message) (\(.pointer))"')
    echo "  Error updating app: $error" >&2
    if [[ -n "$errors" ]]; then
      echo "$errors" >&2
    fi
    return 1
  fi

  local perms_updated
  perms_updated=$(echo "$response" | jq -r '.permissions_updated')
  echo "  Updated: $app_id (permissions_updated=$perms_updated)"

  if [[ "$perms_updated" == "true" ]]; then
    echo "  ⚠ Scopes changed — reinstall the app:"
    echo "    https://api.slack.com/apps/$app_id/install-on-team"
  fi
}

link_app() {
  local agent="$1"
  local app_id="$2"

  local state
  state=$(load_state)
  state=$(echo "$state" | jq \
    --arg agent "$agent" \
    --arg app_id "$app_id" \
    --arg linked "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '.[$agent] = (.[$agent] // {}) | .[$agent].app_id = $app_id | .[$agent].linked = $linked')
  save_state "$state"
  echo "  Linked agent-$agent → $app_id"
}

show_status() {
  local state
  state=$(load_state)
  local agents_filter="${1:-}"

  echo "Slack App Status"
  echo "================"
  echo ""

  for agent in "${!AGENTS[@]}"; do
    if [[ -n "$agents_filter" && "$agent" != "$agents_filter" ]]; then
      continue
    fi

    local app_id
    app_id=$(echo "$state" | jq -r ".\"$agent\".app_id // empty")
    local manifest_name
    manifest_name=$(yq '.display_information.name' "${AGENTS[$agent]}" 2>/dev/null || echo "?")

    if [[ -n "$app_id" ]]; then
      echo "  $agent ($manifest_name): $app_id"
      echo "    Settings: https://api.slack.com/apps/$app_id"
      echo "    Events:   https://api.slack.com/apps/$app_id/event-subscriptions"
    else
      echo "  $agent ($manifest_name): not created"
    fi
    echo ""
  done
}

validate_manifest() {
  local agent="$1"
  local manifest_path="${AGENTS[$agent]}"

  echo "  Validating $manifest_path..."
  local manifest_json
  manifest_json=$(manifest_to_json "$manifest_path")

  local response
  response=$(slack_api "apps.manifest.validate" -d "{\"manifest\": $manifest_json}")

  local ok
  ok=$(echo "$response" | jq -r '.ok')

  if [[ "$ok" == "true" ]]; then
    echo "  Valid"
  else
    local errors
    errors=$(echo "$response" | jq -r '.errors // [] | .[] | "    - \(.message) (\(.pointer))"')
    echo "  Invalid:" >&2
    echo "$errors" >&2
    return 1
  fi
}

usage() {
  echo "Usage: $0 <command> [agent]"
  echo ""
  echo "Commands:"
  echo "  create [agent]    Create Slack app(s) from manifest(s)"
  echo "  update [agent]    Update existing app(s) from manifest(s)"
  echo "  link <agent> <id> Register an existing app ID for an agent"
  echo "  validate [agent]  Validate manifest(s) against Slack API"
  echo "  status [agent]    Show app status"
  echo ""
  echo "Agents: ${!AGENTS[*]}"
  echo ""
  echo "Environment:"
  echo "  SLACK_CONFIG_TOKEN  App configuration token (required for create/update/validate)"
}

main() {
  check_deps

  local command="${1:-}"
  local agent="${2:-}"

  case "$command" in
    create)
      check_token
      if [[ -n "$agent" ]]; then
        if [[ -z "${AGENTS[$agent]:-}" ]]; then
          echo "Unknown agent: $agent" >&2; exit 1
        fi
        echo "Creating agent-$agent..."
        create_app "$agent"
      else
        for a in "${!AGENTS[@]}"; do
          echo "Creating agent-$a..."
          create_app "$a"
          echo ""
        done
      fi
      ;;
    update)
      check_token
      if [[ -n "$agent" ]]; then
        if [[ -z "${AGENTS[$agent]:-}" ]]; then
          echo "Unknown agent: $agent" >&2; exit 1
        fi
        echo "Updating agent-$agent..."
        update_app "$agent"
      else
        for a in "${!AGENTS[@]}"; do
          echo "Updating agent-$a..."
          update_app "$a"
          echo ""
        done
      fi
      ;;
    link)
      local app_id="${3:-}"
      if [[ -z "$agent" || -z "$app_id" ]]; then
        echo "Usage: $0 link <agent> <app_id>" >&2; exit 1
      fi
      if [[ -z "${AGENTS[$agent]:-}" ]]; then
        echo "Unknown agent: $agent" >&2; exit 1
      fi
      link_app "$agent" "$app_id"
      ;;
    validate)
      check_token
      if [[ -n "$agent" ]]; then
        if [[ -z "${AGENTS[$agent]:-}" ]]; then
          echo "Unknown agent: $agent" >&2; exit 1
        fi
        echo "Validating agent-$agent..."
        validate_manifest "$agent"
      else
        for a in "${!AGENTS[@]}"; do
          echo "Validating agent-$a..."
          validate_manifest "$a"
          echo ""
        done
      fi
      ;;
    status)
      show_status "$agent"
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "$@"

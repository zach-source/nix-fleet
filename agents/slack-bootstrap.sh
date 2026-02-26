#!/usr/bin/env bash
# slack-bootstrap.sh — Create/update Slack apps and 1Password secrets for all agents
#
# Prerequisites:
#   1. Slack configuration token:
#      https://api.slack.com/apps → profile icon → "Your configuration tokens"
#      → "Generate Token" for your workspace
#      Export: SLACK_CONFIG_TOKEN="xoxe.xoxp-..."
#
#   2. 1Password CLI (op) signed in for op-* commands
#
# Usage:
#   ./agents/slack-bootstrap.sh create [agent]     # Create Slack app(s) from manifest(s)
#   ./agents/slack-bootstrap.sh update [agent]      # Update existing app(s) from manifest(s)
#   ./agents/slack-bootstrap.sh validate [agent]    # Validate manifest(s) against Slack API
#   ./agents/slack-bootstrap.sh link <agent> <id>   # Register an existing app ID
#   ./agents/slack-bootstrap.sh status [agent]      # Show Slack app status
#   ./agents/slack-bootstrap.sh op-create [agent]   # Create 1Password items with tokens
#   ./agents/slack-bootstrap.sh op-status [agent]   # Show 1Password item status
#   ./agents/slack-bootstrap.sh op-sync [agent]     # Update 1Password items from state
#   ./agents/slack-bootstrap.sh bootstrap [agent]   # Full pipeline: create app → op item → install instructions
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_FILE="$SCRIPT_DIR/.slack-apps.json"
OP_VAULT="Personal Agents"

# Agent definitions: name → manifest path
declare -A AGENTS=(
  [code-review]="$SCRIPT_DIR/code-review/slack-manifest.yaml"
  [pm]="$SCRIPT_DIR/pm/slack-manifest.yaml"
  [marketing]="$SCRIPT_DIR/marketing/slack-manifest.yaml"
  [devops]="$SCRIPT_DIR/devops/slack-manifest.yaml"
  [research]="$SCRIPT_DIR/research/slack-manifest.yaml"
  [security]="$SCRIPT_DIR/security/slack-manifest.yaml"
  [coder]="$SCRIPT_DIR/coder/slack-manifest.yaml"
  [architect]="$SCRIPT_DIR/architect/slack-manifest.yaml"
  [sre]="$SCRIPT_DIR/sre/slack-manifest.yaml"
  [sage]="$SCRIPT_DIR/sage/slack-manifest.yaml"
  [orchestrator]="$SCRIPT_DIR/orchestrator/slack-manifest.yaml"
)

# Shared secrets that every agent gets (fetched from 1Password or env)
SHARED_OPENAI_KEY_ITEM="OPENAI_API_KEY"  # 1Password item in same vault
SHARED_GITHUB_TOKEN_ITEM="GitHub App Key" # or set GITHUB_TOKEN env var

# GitHub fine-grained PAT config
GH_ORG="stigenai"
GH_PAT_DESCRIPTION="nixfleet-agents"
# Repository access: "all" = all repos under GH_ORG (recommended)
GH_REPO_ACCESS="all"
# Required fine-grained PAT permissions (permission:access_level)
GH_PAT_PERMISSIONS=(
  "contents:read"
  "issues:write"
  "pull_requests:write"
  "metadata:read"
)
# GitHub App manifest permissions (same as PAT but different key names)
GH_APP_PERMISSIONS='{
  "contents": "read",
  "issues": "write",
  "pull_requests": "write",
  "metadata": "read"
}'
GH_APP_CALLBACK_PORT=3141
GH_APP_STATE_FILE="$SCRIPT_DIR/.github-apps.json"

# ─── Helpers ────────────────────────────────────────────────────────────────

check_deps() {
  for cmd in yq jq curl; do
    if ! command -v "$cmd" &>/dev/null; then
      echo "Error: $cmd is required but not installed" >&2
      exit 1
    fi
  done
}

check_op() {
  if ! command -v op &>/dev/null; then
    echo "Error: 1Password CLI (op) is required but not installed" >&2
    echo "Install: https://developer.1password.com/docs/cli/get-started/" >&2
    exit 1
  fi
  if ! op account list &>/dev/null 2>&1; then
    echo "Error: Not signed in to 1Password CLI" >&2
    echo "Run: eval \$(op signin)" >&2
    exit 1
  fi
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

# Get the character name from the Slack manifest (e.g., "Marcus", "Lena")
get_agent_name() {
  local agent="$1"
  yq '.display_information.name' "${AGENTS[$agent]}" 2>/dev/null || echo "$agent"
}

# 1Password item title: nixfleet-<lowercase character name>
op_item_title() {
  local agent="$1"
  local name
  name=$(get_agent_name "$agent")
  echo "nixfleet-$(echo "$name" | tr '[:upper:]' '[:lower:]')"
}

manifest_to_json() {
  local manifest_path="$1"
  yq -o json "$manifest_path" | jq -c '.'
}

gen_gateway_token() {
  openssl rand -hex 16
}

for_each_agent() {
  local callback="$1"
  local filter="${2:-}"

  if [[ -n "$filter" ]]; then
    if [[ -z "${AGENTS[$filter]:-}" ]]; then
      echo "Unknown agent: $filter" >&2
      exit 1
    fi
    "$callback" "$filter"
  else
    for a in "${!AGENTS[@]}"; do
      "$callback" "$a"
      echo ""
    done
  fi
}

# ─── Slack App Management ──────────────────────────────────────────────────

create_app() {
  local agent="$1"
  local manifest_path="${AGENTS[$agent]}"
  local name
  name=$(get_agent_name "$agent")

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

  echo "  Creating Slack app '$name' from $manifest_path..."
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
  echo "  Next steps for $name (agent-$agent):"
  echo "    1. Visit: https://api.slack.com/apps/$app_id/general"
  echo "       -> Generate App-Level Token with 'connections:write' scope"
  echo "    2. Install: $oauth_url"
  echo "    3. Run: $0 op-create $agent"
  echo "       -> Creates 1Password item with all tokens"
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
    echo "  Warning: Scopes changed — reinstall the app:"
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
  echo "  Linked agent-$agent -> $app_id"
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

    local app_id name op_title
    app_id=$(echo "$state" | jq -r ".\"$agent\".app_id // empty")
    name=$(get_agent_name "$agent")
    op_title=$(op_item_title "$agent")

    # Check 1Password item
    local op_status="?"
    if command -v op &>/dev/null; then
      if op item get "$op_title" --vault "$OP_VAULT" &>/dev/null 2>&1; then
        op_status="exists"
      else
        op_status="missing"
      fi
    fi

    if [[ -n "$app_id" ]]; then
      echo "  $agent ($name): $app_id"
      echo "    Settings:  https://api.slack.com/apps/$app_id"
      echo "    Events:    https://api.slack.com/apps/$app_id/event-subscriptions"
      echo "    1Password: $op_title ($op_status)"
    else
      echo "  $agent ($name): not created"
      echo "    1Password: $op_title ($op_status)"
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

# ─── 1Password Management ──────────────────────────────────────────────────

# Fetch a shared secret value from 1Password or environment
get_shared_secret() {
  local field="$1"
  local env_var="$2"
  local op_item="${3:-}"

  # Prefer environment variable
  if [[ -n "${!env_var:-}" ]]; then
    echo "${!env_var}"
    return
  fi

  # Fall back to 1Password
  if [[ -n "$op_item" ]]; then
    local val
    val=$(op item get "$op_item" --vault "$OP_VAULT" --fields "$field" 2>/dev/null || true)
    if [[ -n "$val" ]]; then
      echo "$val"
      return
    fi
  fi

  echo ""
}

op_create_item() {
  local agent="$1"
  local name op_title
  name=$(get_agent_name "$agent")
  op_title=$(op_item_title "$agent")

  echo "  Setting up 1Password item: $op_title"

  # Check if item already exists
  if op item get "$op_title" --vault "$OP_VAULT" &>/dev/null 2>&1; then
    echo "  Item already exists. Use 'op-sync' to update fields."
    return 0
  fi

  # Gather tokens — prompt for Slack tokens since they require manual steps
  local slack_bot_token slack_app_token openai_key github_token gateway_token

  # Check state file for any stored credentials from create
  local state
  state=$(load_state)

  echo ""
  echo "  Enter tokens for $name (agent-$agent)."
  echo "  (Leave blank to skip — you can update later with 'op-sync')"
  echo ""

  read -r -p "  SLACK_BOT_TOKEN (xoxb-...): " slack_bot_token
  read -r -p "  SLACK_APP_TOKEN (xapp-...): " slack_app_token

  # Try to get shared secrets automatically
  openai_key=$(get_shared_secret "credential" "OPENAI_API_KEY" "$SHARED_OPENAI_KEY_ITEM")
  if [[ -n "$openai_key" ]]; then
    echo "  OPENAI_API_KEY: found in 1Password ($SHARED_OPENAI_KEY_ITEM)"
  else
    read -r -p "  OPENAI_API_KEY (sk-...): " openai_key
  fi

  github_token=$(get_shared_secret "credential" "GITHUB_TOKEN" "$SHARED_GITHUB_TOKEN_ITEM")
  if [[ -n "$github_token" ]]; then
    echo "  GITHUB_TOKEN: found in 1Password ($SHARED_GITHUB_TOKEN_ITEM)"
  else
    read -r -p "  GITHUB_TOKEN (github_pat_...): " github_token
  fi

  # Generate a gateway token automatically
  gateway_token=$(gen_gateway_token)
  echo "  OPENCLAW_GATEWAY_TOKEN: generated ($(echo "$gateway_token" | head -c 8)...)"

  echo ""
  echo "  Creating 1Password item..."

  # Build op item create command with available fields
  local -a op_args=(
    op item create
    --category "API Credential"
    --vault "$OP_VAULT"
    --title "$op_title"
  )

  [[ -n "$slack_bot_token" ]] && op_args+=("SLACK_BOT_TOKEN=$slack_bot_token")
  [[ -n "$slack_app_token" ]] && op_args+=("SLACK_APP_TOKEN=$slack_app_token")
  [[ -n "$openai_key" ]] && op_args+=("OPENAI_API_KEY=$openai_key")
  [[ -n "$github_token" ]] && op_args+=("GITHUB_TOKEN=$github_token")
  [[ -n "$gateway_token" ]] && op_args+=("OPENCLAW_GATEWAY_TOKEN=$gateway_token")

  "${op_args[@]}" --format=json | jq -r '.id' | {
    read -r item_id
    echo "  Created: $op_title ($item_id)"
  }

  # Save op item title in state
  state=$(load_state)
  state=$(echo "$state" | jq \
    --arg agent "$agent" \
    --arg op_title "$op_title" \
    '.[$agent].op_item = $op_title')
  save_state "$state"

  echo ""
  echo "  OnePasswordItem CRD should reference:"
  echo "    itemPath: \"vaults/$OP_VAULT/items/$op_title\""
}

op_sync_item() {
  local agent="$1"
  local name op_title
  name=$(get_agent_name "$agent")
  op_title=$(op_item_title "$agent")

  echo "  Syncing 1Password item: $op_title"

  if ! op item get "$op_title" --vault "$OP_VAULT" &>/dev/null 2>&1; then
    echo "  Item does not exist. Use 'op-create' first." >&2
    return 1
  fi

  # Show current fields
  echo "  Current fields:"
  op item get "$op_title" --vault "$OP_VAULT" --format=json | jq -r '
    .fields[]
    | select(.value != null and .value != "")
    | "    \(.label) = \(.value | .[0:8])..."
  '

  echo ""
  echo "  Enter new values (leave blank to keep existing):"
  echo ""

  local -a edit_args=()

  read -r -p "  SLACK_BOT_TOKEN: " val
  [[ -n "$val" ]] && edit_args+=("SLACK_BOT_TOKEN=$val")

  read -r -p "  SLACK_APP_TOKEN: " val
  [[ -n "$val" ]] && edit_args+=("SLACK_APP_TOKEN=$val")

  read -r -p "  OPENAI_API_KEY: " val
  [[ -n "$val" ]] && edit_args+=("OPENAI_API_KEY=$val")

  read -r -p "  GITHUB_TOKEN: " val
  [[ -n "$val" ]] && edit_args+=("GITHUB_TOKEN=$val")

  read -r -p "  OPENCLAW_GATEWAY_TOKEN: " val
  [[ -n "$val" ]] && edit_args+=("OPENCLAW_GATEWAY_TOKEN=$val")

  if [[ ${#edit_args[@]} -eq 0 ]]; then
    echo "  No changes."
    return 0
  fi

  op item edit "$op_title" --vault "$OP_VAULT" "${edit_args[@]}" --format=json | jq -r '.id' | {
    read -r item_id
    echo "  Updated: $op_title ($item_id)"
  }
}

op_show_status() {
  local agents_filter="${1:-}"

  echo "1Password Status (vault: $OP_VAULT)"
  echo "====================================="
  echo ""

  for agent in "${!AGENTS[@]}"; do
    if [[ -n "$agents_filter" && "$agent" != "$agents_filter" ]]; then
      continue
    fi

    local name op_title
    name=$(get_agent_name "$agent")
    op_title=$(op_item_title "$agent")

    local item_json
    item_json=$(op item get "$op_title" --vault "$OP_VAULT" --format=json 2>/dev/null || echo "")

    if [[ -z "$item_json" ]]; then
      echo "  $agent ($name): $op_title — MISSING"
      echo "    Run: $0 op-create $agent"
    else
      local fields
      fields=$(echo "$item_json" | jq -r '
        [.fields[]
         | select(.value != null and .value != "")
         | .label
        ] | join(", ")
      ')
      echo "  $agent ($name): $op_title"
      echo "    Fields: $fields"

      # Check for missing required fields
      local -a required=(SLACK_BOT_TOKEN SLACK_APP_TOKEN OPENAI_API_KEY OPENCLAW_GATEWAY_TOKEN)
      local missing=()
      for field in "${required[@]}"; do
        local val
        val=$(echo "$item_json" | jq -r ".fields[] | select(.label == \"$field\") | .value // empty")
        if [[ -z "$val" ]]; then
          missing+=("$field")
        fi
      done
      if [[ ${#missing[@]} -gt 0 ]]; then
        echo "    Missing: ${missing[*]}"
      fi
    fi
    echo ""
  done
}

# ─── GitHub PAT Management ────────────────────────────────────────────────

gh_setup() {
  echo "GitHub Fine-Grained PAT Setup"
  echo "=============================="
  echo ""
  echo "Create a fine-grained PAT at:"
  echo "  https://github.com/settings/personal-access-tokens/new"
  echo ""
  echo "Configuration:"
  echo "  Token name:       $GH_PAT_DESCRIPTION"
  echo "  Expiration:       90 days (or custom)"
  echo "  Resource owner:   $GH_ORG"
  echo "  Repository access: All repositories under $GH_ORG"
  echo ""
  echo "  Permissions (Repository):"
  for perm in "${GH_PAT_PERMISSIONS[@]}"; do
    local name="${perm%%:*}"
    local level="${perm##*:}"
    local display_name="${name//_/ }"
    display_name="$(echo "$display_name" | sed 's/\b\(.\)/\u\1/g')"
    local display_level
    case "$level" in
      read)  display_level="Read-only" ;;
      write) display_level="Read and write" ;;
      *)     display_level="$level" ;;
    esac
    echo "    $display_name: $display_level"
  done
  echo ""
  echo "After creating the PAT:"
  echo "  1. Copy the github_pat_... token"
  echo "  2. Update 1Password items:"
  echo "     $0 gh-update-op"
  echo "  3. Restart agent pods to pick up new token"
  echo ""

  # If an existing token is available, offer to check it
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    echo "GITHUB_TOKEN is set in environment. Checking permissions..."
    echo ""
    gh_check
  fi
}

gh_check() {
  local token="${GITHUB_TOKEN:-}"
  if [[ -z "$token" ]]; then
    # Try to get from 1Password
    if command -v op &>/dev/null; then
      token=$(op item get "$SHARED_GITHUB_TOKEN_ITEM" --vault "$OP_VAULT" --fields credential 2>/dev/null || true)
    fi
  fi

  if [[ -z "$token" ]]; then
    echo "Error: No GitHub token found." >&2
    echo "Set GITHUB_TOKEN env var or ensure 1Password item '$SHARED_GITHUB_TOKEN_ITEM' exists." >&2
    return 1
  fi

  echo "GitHub PAT Permission Check"
  echo "==========================="
  echo ""

  # Check token info
  local user_response
  user_response=$(curl -s -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
    "https://api.github.com/user" 2>&1)
  local login
  login=$(echo "$user_response" | jq -r '.login // "unknown"')
  echo "  Authenticated as: $login"
  echo ""

  # Fetch all repos under the org
  echo "  Fetching repos for $GH_ORG..."
  local repos_json
  repos_json=$(curl -s -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
    "https://api.github.com/orgs/$GH_ORG/repos?per_page=100&sort=name" 2>&1)
  local repo_names
  repo_names=$(echo "$repos_json" | jq -r '.[].name // empty' 2>/dev/null)

  if [[ -z "$repo_names" ]]; then
    echo "  Error: Could not list repos for $GH_ORG (token may lack org access)" >&2
    echo "  Response: $(echo "$repos_json" | jq -r '.message // "unknown"' 2>/dev/null)" >&2
    return 1
  fi

  local repo_count
  repo_count=$(echo "$repo_names" | wc -l | tr -d ' ')
  echo "  Found $repo_count repos"
  echo ""

  local all_ok=true

  while IFS= read -r repo; do
    [[ -z "$repo" ]] && continue
    echo "  $GH_ORG/$repo:"

    # Check repo access
    local repo_response
    repo_response=$(curl -s -w "\n%{http_code}" -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/$GH_ORG/$repo" 2>&1)
    local http_code
    http_code=$(echo "$repo_response" | tail -1)
    local repo_body
    repo_body=$(echo "$repo_response" | sed '$d')

    if [[ "$http_code" != "200" ]]; then
      echo "    Repo access: FAIL (HTTP $http_code)"
      all_ok=false
      continue
    fi

    local permissions
    permissions=$(echo "$repo_body" | jq -c '.permissions // {}')
    echo "    Repo access: OK"
    echo "    Permissions: $permissions"

    # Check contents:read via GraphQL (the one that was failing)
    local graphql_response
    graphql_response=$(curl -s -H "Authorization: Bearer $token" -H "Content-Type: application/json" \
      "https://api.github.com/graphql" \
      -d "{\"query\": \"{ repository(owner: \\\"$GH_ORG\\\", name: \\\"$repo\\\") { defaultBranchRef { name } } }\"}" 2>&1)
    local branch
    branch=$(echo "$graphql_response" | jq -r '.data.repository.defaultBranchRef.name // empty')
    local graphql_error
    graphql_error=$(echo "$graphql_response" | jq -r '.errors[0].message // empty')

    if [[ -n "$branch" ]]; then
      echo "    Contents (GraphQL): OK (branch: $branch)"
    elif [[ -n "$graphql_error" ]]; then
      echo "    Contents (GraphQL): FAIL — $graphql_error"
      all_ok=false
    else
      # Empty repo (no commits) — defaultBranchRef is null, but access is fine
      echo "    Contents (GraphQL): OK (empty repo — no default branch)"
    fi

    # Check issues:write
    local issues_response
    issues_response=$(curl -s -w "\n%{http_code}" -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/$GH_ORG/$repo/issues?per_page=1&state=all" 2>&1)
    http_code=$(echo "$issues_response" | tail -1)
    if [[ "$http_code" == "200" ]]; then
      echo "    Issues (read): OK"
    else
      echo "    Issues (read): FAIL (HTTP $http_code)"
      all_ok=false
    fi

    # Check pull_requests:read
    local pr_response
    pr_response=$(curl -s -w "\n%{http_code}" -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/$GH_ORG/$repo/pulls?per_page=1&state=all" 2>&1)
    http_code=$(echo "$pr_response" | tail -1)
    if [[ "$http_code" == "200" ]]; then
      echo "    Pull requests (read): OK"
    else
      echo "    Pull requests (read): FAIL (HTTP $http_code)"
      all_ok=false
    fi

    echo ""
  done <<< "$repo_names"

  if $all_ok; then
    echo "  All checks passed."
  else
    echo "  Some checks FAILED. Recreate the PAT with the required permissions:"
    echo "    $0 gh-setup"
  fi

  return 0
}

gh_update_op() {
  check_op

  echo "Update GitHub PAT in 1Password"
  echo "==============================="
  echo ""
  read -r -p "  Paste new GITHUB_TOKEN (github_pat_...): " new_token

  if [[ -z "$new_token" ]]; then
    echo "  No token provided. Aborting."
    return 1
  fi

  # Verify the token works before saving
  echo ""
  echo "  Verifying token..."
  local user_response
  user_response=$(curl -s -H "Authorization: Bearer $new_token" "https://api.github.com/user" 2>&1)
  local login
  login=$(echo "$user_response" | jq -r '.login // empty')
  if [[ -z "$login" ]]; then
    echo "  Error: Token is invalid or expired." >&2
    return 1
  fi
  echo "  Authenticated as: $login"

  # Update shared 1Password item
  echo "  Updating shared item: $SHARED_GITHUB_TOKEN_ITEM..."
  if op item get "$SHARED_GITHUB_TOKEN_ITEM" --vault "$OP_VAULT" &>/dev/null 2>&1; then
    op item edit "$SHARED_GITHUB_TOKEN_ITEM" --vault "$OP_VAULT" "credential=$new_token" &>/dev/null
    echo "  Updated: $SHARED_GITHUB_TOKEN_ITEM"
  else
    echo "  Shared item not found, skipping."
  fi

  # Update each agent's 1Password item
  echo ""
  for agent in "${!AGENTS[@]}"; do
    local op_title
    op_title=$(op_item_title "$agent")
    if op item get "$op_title" --vault "$OP_VAULT" &>/dev/null 2>&1; then
      op item edit "$op_title" --vault "$OP_VAULT" "GITHUB_TOKEN=$new_token" &>/dev/null
      echo "  Updated: $op_title"
    else
      echo "  Skipped: $op_title (not found)"
    fi
  done

  echo ""
  echo "  Token updated in all 1Password items."
  echo "  Restart agent pods to pick up new token:"
  echo "    kubectl delete pod -n agent-pm agent-pm-0"
  echo "    # ... (or restart all agents)"
  echo ""
  echo "  Run '$0 gh-check' to verify permissions."
}

# ─── GitHub App Management ────────────────────────────────────────────────

load_gh_app_state() {
  if [[ -f "$GH_APP_STATE_FILE" ]]; then
    cat "$GH_APP_STATE_FILE"
  else
    echo '{}'
  fi
}

save_gh_app_state() {
  echo "$1" | jq '.' > "$GH_APP_STATE_FILE"
}

get_gh_app_id() {
  local agent="$1"
  load_gh_app_state | jq -r ".\"$agent\".app_id // empty"
}

# Agent display name for GitHub App (e.g., "Marcus PM" → "marcus-pm")
gh_app_slug() {
  local agent="$1"
  local name
  name=$(get_agent_name "$agent")
  echo "${name,,}-${agent}" | tr ' ' '-'
}

gh_app_create_one() {
  local agent="$1"
  local name
  name=$(get_agent_name "$agent")
  local app_name="${name} (NixFleet)"

  local existing_id
  existing_id=$(get_gh_app_id "$agent")
  if [[ -n "$existing_id" ]]; then
    echo "  GitHub App already exists for $agent: $existing_id"
    echo "  Settings: https://github.com/organizations/$GH_ORG/settings/apps"
    return 0
  fi

  echo "  Creating GitHub App: $app_name"

  # Build manifest
  local manifest
  manifest=$(jq -n \
    --arg name "$app_name" \
    --arg url "https://github.com/$GH_ORG" \
    --argjson port "$GH_APP_CALLBACK_PORT" \
    --argjson perms "$GH_APP_PERMISSIONS" \
    '{
      name: $name,
      url: $url,
      hook_attributes: { active: false },
      redirect_url: "http://127.0.0.1:\($port)/callback",
      public: false,
      default_events: [],
      default_permissions: $perms
    }')

  local output_file
  output_file=$(mktemp)
  trap "rm -f '$output_file'" RETURN

  # Start manifest flow server in background
  python3 "$SCRIPT_DIR/lib/gh-manifest-server.py" \
    "$manifest" "$GH_ORG" "$output_file" "$GH_APP_CALLBACK_PORT" &
  local server_pid=$!

  sleep 1

  # Open browser
  local url="http://127.0.0.1:$GH_APP_CALLBACK_PORT"
  echo "  Opening browser: $url"
  if command -v open &>/dev/null; then
    open "$url"
  elif command -v xdg-open &>/dev/null; then
    xdg-open "$url"
  else
    echo "  Please open $url in your browser"
  fi

  echo "  Waiting for GitHub approval..."
  wait "$server_pid" 2>/dev/null || true

  # Read result
  if [[ ! -f "$output_file" ]] || [[ ! -s "$output_file" ]]; then
    echo "  Error: No credentials received" >&2
    return 1
  fi

  local app_id slug pem client_id html_url
  app_id=$(jq -r '.id' "$output_file")
  slug=$(jq -r '.slug' "$output_file")
  pem=$(jq -r '.pem' "$output_file")
  client_id=$(jq -r '.client_id' "$output_file")
  html_url=$(jq -r '.html_url' "$output_file")

  echo ""
  echo "  Created: $app_name"
  echo "  App ID:  $app_id"
  echo "  Slug:    $slug"
  echo "  URL:     $html_url"

  # Save to state
  local state
  state=$(load_gh_app_state)
  local pem_b64
  pem_b64=$(echo "$pem" | base64)
  state=$(echo "$state" | jq \
    --arg agent "$agent" \
    --arg app_id "$app_id" \
    --arg slug "$slug" \
    --arg pem_b64 "$pem_b64" \
    --arg client_id "$client_id" \
    --arg html_url "$html_url" \
    --arg created "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '.[$agent] = {app_id: ($app_id | tonumber), slug: $slug, pem_b64: $pem_b64, client_id: $client_id, html_url: $html_url, created: $created}')
  save_gh_app_state "$state"

  echo ""
  echo "  Next steps:"
  echo "    1. Install the app on $GH_ORG:"
  echo "       https://github.com/organizations/$GH_ORG/settings/installations"
  echo "       Or: $0 gh-app-install $agent"
  echo "    2. Store credentials in 1Password:"
  echo "       $0 gh-app-op $agent"
}

gh_app_install_one() {
  local agent="$1"
  local app_id
  app_id=$(get_gh_app_id "$agent")

  if [[ -z "$app_id" ]]; then
    echo "  No GitHub App for $agent. Run 'gh-app-create $agent' first." >&2
    return 1
  fi

  local state
  state=$(load_gh_app_state)
  local slug
  slug=$(echo "$state" | jq -r ".\"$agent\".slug // empty")

  echo "  Installing $slug (app $app_id) on $GH_ORG..."
  echo ""
  echo "  Open this URL to install:"
  echo "  https://github.com/apps/$slug/installations/new/permissions?target_id=$(
    curl -s -H "Accept: application/vnd.github+json" \
      "https://api.github.com/orgs/$GH_ORG" 2>/dev/null | jq -r '.id // empty'
  )"
  echo ""

  if command -v open &>/dev/null; then
    open "https://github.com/apps/$slug/installations/new"
  elif command -v xdg-open &>/dev/null; then
    xdg-open "https://github.com/apps/$slug/installations/new"
  fi

  echo "  After installing, get the installation ID:"
  echo "    The URL will be: https://github.com/organizations/$GH_ORG/settings/installations/<ID>"
  echo ""
  read -r -p "  Enter installation ID: " installation_id

  if [[ -z "$installation_id" ]]; then
    echo "  Skipped."
    return 0
  fi

  state=$(load_gh_app_state)
  state=$(echo "$state" | jq \
    --arg agent "$agent" \
    --arg inst_id "$installation_id" \
    '.[$agent].installation_id = ($inst_id | tonumber)')
  save_gh_app_state "$state"

  echo "  Saved installation ID: $installation_id"
}

gh_app_op_one() {
  local agent="$1"
  check_op

  local state
  state=$(load_gh_app_state)
  local app_id pem_b64 installation_id
  app_id=$(echo "$state" | jq -r ".\"$agent\".app_id // empty")
  pem_b64=$(echo "$state" | jq -r ".\"$agent\".pem_b64 // empty")
  installation_id=$(echo "$state" | jq -r ".\"$agent\".installation_id // empty")

  if [[ -z "$app_id" ]]; then
    echo "  No GitHub App for $agent. Run 'gh-app-create $agent' first." >&2
    return 1
  fi

  if [[ -z "$installation_id" ]]; then
    echo "  No installation ID for $agent. Run 'gh-app-install $agent' first." >&2
    return 1
  fi

  local op_title
  op_title=$(op_item_title "$agent")
  echo "  Updating 1Password item: $op_title"

  if ! op item get "$op_title" --vault "$OP_VAULT" &>/dev/null 2>&1; then
    echo "  Item does not exist. Run 'op-create $agent' first." >&2
    return 1
  fi

  # Update the 1Password item with GitHub App credentials
  op item edit "$op_title" --vault "$OP_VAULT" \
    "GITHUB_APP_ID=$app_id" \
    "GITHUB_APP_PRIVATE_KEY_B64=$pem_b64" \
    "GITHUB_APP_INSTALLATION_ID=$installation_id" \
    &>/dev/null

  echo "  Updated: $op_title"
  echo "    GITHUB_APP_ID=$app_id"
  echo "    GITHUB_APP_INSTALLATION_ID=$installation_id"
  echo "    GITHUB_APP_PRIVATE_KEY_B64=(${#pem_b64} chars)"
  echo ""
  echo "  Restart the pod to pick up new credentials:"
  echo "    kubectl delete pod -n agent-$agent agent-$agent-0"
}

gh_app_status() {
  local filter="${1:-}"

  echo "GitHub App Status"
  echo "================="
  echo ""

  local state
  state=$(load_gh_app_state)

  for agent in "${!AGENTS[@]}"; do
    if [[ -n "$filter" && "$agent" != "$filter" ]]; then
      continue
    fi

    local name app_id slug installation_id
    name=$(get_agent_name "$agent")
    app_id=$(echo "$state" | jq -r ".\"$agent\".app_id // empty")
    slug=$(echo "$state" | jq -r ".\"$agent\".slug // empty")
    installation_id=$(echo "$state" | jq -r ".\"$agent\".installation_id // empty")

    if [[ -n "$app_id" ]]; then
      echo "  $agent ($name): $slug (ID: $app_id)"
      echo "    Settings:        https://github.com/organizations/$GH_ORG/settings/apps/$slug"
      if [[ -n "$installation_id" ]]; then
        echo "    Installation ID: $installation_id"
      else
        echo "    Installation:    NOT INSTALLED — run: $0 gh-app-install $agent"
      fi
    else
      echo "  $agent ($name): not created"
      echo "    Run: $0 gh-app-create $agent"
    fi
    echo ""
  done
}

# ─── Full Bootstrap Pipeline ───────────────────────────────────────────────

bootstrap_agent() {
  local agent="$1"
  local name
  name=$(get_agent_name "$agent")

  echo "=== Bootstrapping $name (agent-$agent) ==="
  echo ""

  # Step 0: Check GitHub PAT
  echo "[0/3] Checking GitHub PAT..."
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    gh_check 2>/dev/null && echo "" || echo "  Warning: PAT check failed — run '$0 gh-setup' to fix"
  else
    echo "  GITHUB_TOKEN not set — will prompt during op-create"
  fi
  echo ""

  # Step 1: Create Slack app if needed
  local app_id
  app_id=$(get_app_id "$agent")
  if [[ -z "$app_id" ]]; then
    echo "[1/3] Creating Slack app..."
    create_app "$agent"
    app_id=$(get_app_id "$agent")
    echo ""
    echo "  MANUAL STEPS REQUIRED before continuing:"
    echo "    1. Generate App-Level Token at: https://api.slack.com/apps/$app_id/general"
    echo "       -> Click 'Generate Token and Scopes'"
    echo "       -> Add scope: connections:write"
    echo "       -> Copy the xapp-... token"
    echo "    2. Install the app to your workspace:"
    state=$(load_state)
    local oauth_url
    oauth_url=$(echo "$state" | jq -r ".\"$agent\".oauth_url // empty")
    echo "       -> Visit: $oauth_url"
    echo "       -> Copy the Bot User OAuth Token (xoxb-...)"
    echo ""
    read -r -p "  Press Enter when ready to continue (or Ctrl-C to stop)..."
    echo ""
  else
    echo "[1/3] Slack app exists: $app_id"
  fi

  # Step 2: Create 1Password item
  local op_title
  op_title=$(op_item_title "$agent")
  echo ""
  echo "[2/3] 1Password item..."
  if op item get "$op_title" --vault "$OP_VAULT" &>/dev/null 2>&1; then
    echo "  Item exists: $op_title"
  else
    op_create_item "$agent"
  fi

  # Step 3: Show K8s CRD reference
  echo ""
  echo "[3/3] Kubernetes OnePasswordItem CRD:"
  echo "  Ensure agent-$agent/onepassword-item.yaml references:"
  echo ""
  echo "    spec:"
  echo "      itemPath: \"vaults/$OP_VAULT/items/$op_title\""
  echo ""
  echo "  Then restart the pod:"
  echo "    kubectl delete pod -n agent-$agent agent-$agent-0"
  echo ""
  echo "=== $name bootstrap complete ==="
}

# ─── Main ──────────────────────────────────────────────────────────────────

usage() {
  echo "Usage: $0 <command> [agent]"
  echo ""
  echo "Slack App Commands:"
  echo "  create [agent]    Create Slack app(s) from manifest(s)"
  echo "  update [agent]    Update existing app(s) from manifest(s)"
  echo "  link <agent> <id> Register an existing app ID for an agent"
  echo "  validate [agent]  Validate manifest(s) against Slack API"
  echo "  status [agent]    Show Slack app + 1Password status"
  echo ""
  echo "GitHub App Commands (recommended):"
  echo "  gh-app-create [agent]   Create GitHub App(s) via manifest flow"
  echo "  gh-app-install [agent]  Install app on $GH_ORG org (opens browser)"
  echo "  gh-app-op [agent]       Store app credentials in 1Password"
  echo "  gh-app-status [agent]   Show GitHub App status"
  echo ""
  echo "GitHub PAT Commands (legacy):"
  echo "  gh-setup          Show PAT creation instructions with required permissions"
  echo "  gh-check          Verify current PAT has all required permissions"
  echo "  gh-update-op      Update GitHub PAT in all 1Password agent items"
  echo ""
  echo "1Password Commands:"
  echo "  op-create [agent] Create 1Password item(s) with agent tokens"
  echo "  op-sync [agent]   Update fields in existing 1Password item(s)"
  echo "  op-status [agent] Show 1Password item status and missing fields"
  echo ""
  echo "Pipeline:"
  echo "  bootstrap [agent] Full pipeline: create app -> 1Password -> K8s instructions"
  echo ""
  echo "Agents: ${!AGENTS[*]}"
  echo ""
  echo "Environment:"
  echo "  SLACK_CONFIG_TOKEN  Slack app configuration token (for create/update/validate)"
  echo "  OPENAI_API_KEY      Override shared OpenAI key (otherwise pulled from 1Password)"
  echo "  GITHUB_TOKEN        Override shared GitHub token (otherwise pulled from 1Password)"
}

main() {
  check_deps

  local command="${1:-}"
  local agent="${2:-}"

  case "$command" in
    create)
      check_token
      for_each_agent create_app "$agent"
      ;;
    update)
      check_token
      for_each_agent update_app "$agent"
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
      for_each_agent validate_manifest "$agent"
      ;;
    status)
      show_status "$agent"
      ;;
    gh-app-create)
      for_each_agent gh_app_create_one "$agent"
      ;;
    gh-app-install)
      for_each_agent gh_app_install_one "$agent"
      ;;
    gh-app-op)
      check_op
      for_each_agent gh_app_op_one "$agent"
      ;;
    gh-app-status)
      gh_app_status "$agent"
      ;;
    gh-setup)
      gh_setup
      ;;
    gh-check)
      gh_check
      ;;
    gh-update-op)
      check_op
      gh_update_op
      ;;
    op-create)
      check_op
      for_each_agent op_create_item "$agent"
      ;;
    op-sync)
      check_op
      for_each_agent op_sync_item "$agent"
      ;;
    op-status)
      check_op
      op_show_status "$agent"
      ;;
    bootstrap)
      check_token
      check_op
      for_each_agent bootstrap_agent "$agent"
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "$@"

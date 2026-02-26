#!/usr/bin/env bash
set -euo pipefail

# Creates GitHub Apps for NixFleet agents via the manifest flow.
# Usage: ./github-app-create.sh <agent-name> [org]
#
# Starts a local HTTP server, opens the GitHub manifest approval page,
# captures the conversion code, exchanges for credentials, and stores
# the private key in 1Password.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
AGENT="${1:?Usage: $0 <agent-name> [org]}"
ORG="${2:-stigenai}"
PORT=3457
STATE_FILE="$SCRIPT_DIR/.github-apps.json"

# Agent display names
declare -A NAMES=(
  [code-review]="Lena"
  [pm]="Marcus"
  [marketing]="Sophie"
  [personal]="Ada"
  [devops]="Kai"
  [research]="Nora"
  [security]="Rex"
  [coder]="Axel"
  [architect]="Theo"
  [sre]="Quinn"
  [sage]="Sage"
  [orchestrator]="Atlas"
)

DISPLAY_NAME="${NAMES[$AGENT]:-$AGENT}"
APP_NAME="nixfleet-${DISPLAY_NAME,,}"

# 1Password item name
OP_ITEM="nixfleet-${DISPLAY_NAME,,}"

echo "=== Creating GitHub App: $APP_NAME ($DISPLAY_NAME) ==="
echo "Org: $ORG"
echo "1Password item: $OP_ITEM"
echo ""

# Build manifest
MANIFEST=$(cat <<EOF
{
  "name": "$APP_NAME",
  "url": "https://github.com/$ORG",
  "hook_attributes": { "active": false },
  "redirect_url": "http://localhost:$PORT/callback",
  "public": false,
  "default_permissions": {
    "contents": "write",
    "issues": "write",
    "pull_requests": "write",
    "metadata": "read",
    "actions": "read"
  },
  "default_events": [
    "issues",
    "pull_request",
    "push"
  ]
}
EOF
)

# Create a temporary HTML page to auto-submit the manifest form
TMPHTML=$(mktemp /tmp/gh-manifest-XXXXX.html)
cat > "$TMPHTML" <<HTMLEOF
<!DOCTYPE html>
<html>
<body>
<form id="f" method="post" action="https://github.com/organizations/$ORG/settings/apps/new">
<input type="hidden" name="manifest" value='$(echo "$MANIFEST" | jq -c .)'>
<p>Creating GitHub App: <strong>$APP_NAME</strong></p>
<p>Click the button if not auto-redirected:</p>
<button type="submit">Create GitHub App</button>
</form>
<script>document.getElementById('f').submit();</script>
</body>
</html>
HTMLEOF

echo "Starting callback server on port $PORT..."

# Start a background server to capture the callback code
CODE_FILE=$(mktemp /tmp/gh-code-XXXXX.txt)
python3 -c "
import http.server, urllib.parse, sys

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        params = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
        code = params.get('code', [''])[0]
        if code:
            with open('$CODE_FILE', 'w') as f:
                f.write(code)
            self.send_response(200)
            self.send_header('Content-Type', 'text/html')
            self.end_headers()
            self.wfile.write(b'<h1>GitHub App created! You can close this tab.</h1>')
            # Shut down after receiving the code
            import threading
            threading.Thread(target=self.server.shutdown).start()
        else:
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b'No code received')
    def log_message(self, *args): pass

httpd = http.server.HTTPServer(('127.0.0.1', $PORT), Handler)
httpd.serve_forever()
" &
SERVER_PID=$!

sleep 1

# Open the manifest form in the browser
echo "Opening browser for approval..."
open "$TMPHTML"

# Wait for the callback
echo "Waiting for GitHub callback (approve in browser)..."
for i in $(seq 1 120); do
  if [ -s "$CODE_FILE" ]; then
    break
  fi
  sleep 1
done

# Clean up server
kill $SERVER_PID 2>/dev/null || true
rm -f "$TMPHTML"

CODE=$(cat "$CODE_FILE" 2>/dev/null)
rm -f "$CODE_FILE"

if [ -z "$CODE" ]; then
  echo "ERROR: No code received within timeout"
  exit 1
fi

echo "Got conversion code. Exchanging for credentials..."

# Exchange code for app credentials
RESULT=$(gh api "app-manifests/$CODE/conversions" --method POST 2>/dev/null)

APP_ID=$(echo "$RESULT" | jq -r '.id')
APP_SLUG=$(echo "$RESULT" | jq -r '.slug')
CLIENT_ID=$(echo "$RESULT" | jq -r '.client_id')
CLIENT_SECRET=$(echo "$RESULT" | jq -r '.client_secret')
PEM=$(echo "$RESULT" | jq -r '.pem')
WEBHOOK_SECRET=$(echo "$RESULT" | jq -r '.webhook_secret')

echo ""
echo "GitHub App created:"
echo "  ID: $APP_ID"
echo "  Slug: $APP_SLUG"
echo "  Client ID: $CLIENT_ID"

# Store private key in 1Password
echo ""
echo "Storing GitHub App private key in 1Password ($OP_ITEM)..."
if op item get "$OP_ITEM" --vault "Personal Agents" --format json >/dev/null 2>&1; then
  op item edit "$OP_ITEM" --vault "Personal Agents" \
    "GITHUB_APP_ID=$APP_ID" \
    "GITHUB_APP_PRIVATE_KEY[password]=$PEM" \
    "GITHUB_APP_CLIENT_ID=$CLIENT_ID" \
    "GITHUB_APP_SLUG=$APP_SLUG" >/dev/null 2>&1
  echo "  Updated existing item"
else
  echo "  WARNING: 1Password item $OP_ITEM not found. Storing key locally."
  echo "$PEM" > "$SCRIPT_DIR/.github-app-$AGENT.pem"
  chmod 600 "$SCRIPT_DIR/.github-app-$AGENT.pem"
fi

# Update state file
if [ -f "$STATE_FILE" ]; then
  EXISTING=$(cat "$STATE_FILE")
else
  EXISTING="{}"
fi
echo "$EXISTING" | jq --arg agent "$AGENT" \
  --arg app_id "$APP_ID" \
  --arg slug "$APP_SLUG" \
  --arg client_id "$CLIENT_ID" \
  --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '.[$agent] = {app_id: ($app_id | tonumber), slug: $slug, client_id: $client_id, created: $ts}' > "$STATE_FILE"

echo ""
echo "=== Done: $APP_NAME (ID: $APP_ID) ==="
echo ""
echo "Next: Install the app on org repos:"
echo "  gh api /app/installations --method POST ..."
echo "  Or visit: https://github.com/organizations/$ORG/settings/installations"

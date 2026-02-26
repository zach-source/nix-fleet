#!/usr/bin/env bash
# gh-app-token.sh — Generate a GitHub App installation token from a private key
#
# Requires: openssl, curl, jq, base64
#
# Environment:
#   GITHUB_APP_ID                 — GitHub App ID (numeric)
#   GITHUB_APP_PRIVATE_KEY_B64    — Base64-encoded PEM private key
#   GITHUB_APP_INSTALLATION_ID    — Installation ID (numeric)
#
# Output: prints the installation token to stdout
set -euo pipefail

APP_ID="${GITHUB_APP_ID:?GITHUB_APP_ID not set}"
PRIVATE_KEY_B64="${GITHUB_APP_PRIVATE_KEY_B64:?GITHUB_APP_PRIVATE_KEY_B64 not set}"
INSTALLATION_ID="${GITHUB_APP_INSTALLATION_ID:?GITHUB_APP_INSTALLATION_ID not set}"

# Decode private key to a temp file
PEM_FILE=$(mktemp)
trap 'rm -f "$PEM_FILE"' EXIT
echo "$PRIVATE_KEY_B64" | base64 -d > "$PEM_FILE"

# Base64url encode (no padding)
b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

# Build JWT (RS256)
NOW=$(date +%s)
HEADER='{"alg":"RS256","typ":"JWT"}'
PAYLOAD="{\"iss\":\"$APP_ID\",\"iat\":$((NOW-60)),\"exp\":$((NOW+600))}"

HEADER_B64=$(echo -n "$HEADER" | b64url)
PAYLOAD_B64=$(echo -n "$PAYLOAD" | b64url)
SIGNATURE=$(echo -n "${HEADER_B64}.${PAYLOAD_B64}" | \
  openssl dgst -sha256 -sign "$PEM_FILE" | b64url)
JWT="${HEADER_B64}.${PAYLOAD_B64}.${SIGNATURE}"

# Exchange JWT for installation token (1-hour lifetime)
RESPONSE=$(curl -sf -X POST \
  -H "Authorization: Bearer $JWT" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/app/installations/$INSTALLATION_ID/access_tokens")

TOKEN=$(echo "$RESPONSE" | jq -r '.token // empty')

if [[ -z "$TOKEN" ]]; then
  echo "Error: Failed to generate installation token" >&2
  echo "$RESPONSE" >&2
  exit 1
fi

echo "$TOKEN"

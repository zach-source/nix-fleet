#!/usr/bin/env bash
set -euo pipefail

# Creates GitHub Apps for ALL NixFleet agents.
# Each app requires brief browser approval.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
AGENTS=(code-review pm marketing personal devops research security coder architect sre sage orchestrator)

echo "=== Creating GitHub Apps for ${#AGENTS[@]} agents ==="
echo "Each app requires brief browser approval."
echo ""

for agent in "${AGENTS[@]}"; do
  echo "--- $agent ---"
  "$SCRIPT_DIR/github-app-create.sh" "$agent" stigenai
  echo ""
  sleep 2
done

echo "=== All done ==="
echo ""
echo "State file: $SCRIPT_DIR/.github-apps.json"
echo ""
echo "Next steps:"
echo "1. Install each app on the org: https://github.com/organizations/stigenai/settings/installations"
echo "2. Restart agent pods to pick up new keys from 1Password"

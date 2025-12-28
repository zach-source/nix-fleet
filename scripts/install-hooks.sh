#!/bin/bash
# Install git hooks for nixfleet development
#
# Usage: ./scripts/install-hooks.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

# Detect if using jj or git
if [ -d "$REPO_ROOT/.jj" ]; then
    HOOKS_DIR="$REPO_ROOT/.jj/repo/store/git/hooks"
elif [ -d "$REPO_ROOT/.git" ]; then
    HOOKS_DIR="$REPO_ROOT/.git/hooks"
else
    echo "Error: Not a git or jj repository"
    exit 1
fi

mkdir -p "$HOOKS_DIR"

echo "Installing git hooks to $HOOKS_DIR..."

# Install pre-commit hook
cp "$SCRIPT_DIR/hooks/pre-commit" "$HOOKS_DIR/pre-commit"
chmod +x "$HOOKS_DIR/pre-commit"

echo "Hooks installed successfully!"
echo ""
echo "The following hooks are now active:"
echo "  - pre-commit: runs golangci-lint before each commit"

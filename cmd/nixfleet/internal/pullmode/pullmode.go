// Package pullmode implements pull-based deployment for NixFleet
// In pull mode, hosts periodically fetch their configuration from a Git repository
// and apply it locally, rather than having a central server push changes.
package pullmode

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"text/template"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// Config holds pull mode configuration
type Config struct {
	// Git repository URL (SSH format: git@github.com:org/repo.git)
	RepoURL string

	// Branch to track
	Branch string

	// Host name (used to find host config in the repo)
	HostName string

	// Path to SSH key for Git access
	SSHKeyPath string

	// Path to age key for secrets decryption
	AgeKeyPath string

	// Pull interval (systemd timer format, e.g., "15min", "1h")
	Interval string

	// Whether to apply on boot
	ApplyOnBoot bool

	// Local path to clone repo to
	RepoPath string

	// Webhook URL for status notifications (optional)
	WebhookURL string

	// Webhook secret for signing (optional)
	WebhookSecret string
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		Branch:      "main",
		SSHKeyPath:  "/run/nixfleet-secrets/github-deploy-key",
		AgeKeyPath:  "/root/.config/age/key.txt",
		Interval:    "15min",
		ApplyOnBoot: true,
		RepoPath:    "/var/lib/nixfleet/repo",
	}
}

// Installer handles pull mode installation on hosts
type Installer struct{}

// NewInstaller creates a new pull mode installer
func NewInstaller() *Installer {
	return &Installer{}
}

// Install sets up pull mode on a host
func (i *Installer) Install(ctx context.Context, client *ssh.Client, config Config) error {
	// Create directories
	if err := i.createDirectories(ctx, client, config); err != nil {
		return fmt.Errorf("creating directories: %w", err)
	}

	// Set up SSH config for Git
	if err := i.setupSSHConfig(ctx, client, config); err != nil {
		return fmt.Errorf("setting up SSH config: %w", err)
	}

	// Clone or update repository
	if err := i.setupRepository(ctx, client, config); err != nil {
		return fmt.Errorf("setting up repository: %w", err)
	}

	// Install pull script
	if err := i.installPullScript(ctx, client, config); err != nil {
		return fmt.Errorf("installing pull script: %w", err)
	}

	// Install systemd units
	if err := i.installSystemdUnits(ctx, client, config); err != nil {
		return fmt.Errorf("installing systemd units: %w", err)
	}

	// Enable and start timer
	if err := i.enableTimer(ctx, client); err != nil {
		return fmt.Errorf("enabling timer: %w", err)
	}

	return nil
}

// Uninstall removes pull mode from a host
func (i *Installer) Uninstall(ctx context.Context, client *ssh.Client) error {
	cmds := []string{
		"systemctl stop nixfleet-pull.timer || true",
		"systemctl disable nixfleet-pull.timer || true",
		"rm -f /etc/systemd/system/nixfleet-pull.service",
		"rm -f /etc/systemd/system/nixfleet-pull.timer",
		"rm -f /usr/local/bin/nixfleet-pull",
		"systemctl daemon-reload",
	}

	for _, cmd := range cmds {
		if _, err := client.ExecSudo(ctx, cmd); err != nil {
			return err
		}
	}

	return nil
}

// Status returns pull mode status on a host
func (i *Installer) Status(ctx context.Context, client *ssh.Client) (*Status, error) {
	status := &Status{}

	// Check if pull mode is installed
	result, err := client.Exec(ctx, "test -f /usr/local/bin/nixfleet-pull && echo installed || echo not-installed")
	if err != nil {
		return nil, err
	}
	status.Installed = result.Stdout == "installed\n"

	if !status.Installed {
		return status, nil
	}

	// Check timer status
	result, err = client.ExecSudo(ctx, "systemctl is-active nixfleet-pull.timer 2>/dev/null || echo inactive")
	if err != nil {
		return nil, err
	}
	status.TimerActive = result.Stdout == "active\n"

	// Get last run time
	result, err = client.ExecSudo(ctx, "systemctl show nixfleet-pull.service --property=ExecMainExitTimestamp --value 2>/dev/null || echo unknown")
	if err == nil {
		status.LastRun = result.Stdout
	}

	// Get last run result
	result, err = client.ExecSudo(ctx, "systemctl show nixfleet-pull.service --property=ExecMainStatus --value 2>/dev/null || echo unknown")
	if err == nil {
		status.LastResult = result.Stdout
	}

	// Get next scheduled run
	result, err = client.ExecSudo(ctx, "systemctl show nixfleet-pull.timer --property=NextElapseUSecRealtime --value 2>/dev/null || echo unknown")
	if err == nil {
		status.NextRun = result.Stdout
	}

	// Get repo info (use safe.directory to handle root-owned repo, wrap in bash because cd is a built-in)
	result, err = client.ExecSudo(ctx, "bash -c 'cd /var/lib/nixfleet/repo && git -c safe.directory=/var/lib/nixfleet/repo rev-parse --short HEAD 2>/dev/null || echo unknown'")
	if err == nil {
		status.CurrentCommit = result.Stdout
	}

	return status, nil
}

// Status represents pull mode status on a host
type Status struct {
	Installed     bool
	TimerActive   bool
	LastRun       string
	LastResult    string
	NextRun       string
	CurrentCommit string
}

func (i *Installer) createDirectories(ctx context.Context, client *ssh.Client, config Config) error {
	cmds := []string{
		"mkdir -p /var/lib/nixfleet",
		"mkdir -p /var/log/nixfleet",
		"mkdir -p /root/.ssh/sockets",
		"chmod 700 /root/.ssh/sockets",
	}

	for _, cmd := range cmds {
		result, err := client.ExecSudo(ctx, cmd)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("command failed: %s", result.Stderr)
		}
	}

	return nil
}

func (i *Installer) setupSSHConfig(ctx context.Context, client *ssh.Client, config Config) error {
	sshConfig := fmt.Sprintf(`# NixFleet pull mode - GitHub access
Host github.com
    HostName github.com
    User git
    IdentityFile %s
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
`, config.SSHKeyPath)

	// Use base64 encoding and bash -c to run entire pipeline with sudo
	encoded := base64Encode([]byte(sshConfig))
	cmd := fmt.Sprintf("bash -c \"echo '%s' | base64 -d > /root/.ssh/config && chmod 600 /root/.ssh/config\"", encoded)
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write SSH config: %s", result.Stderr)
	}

	return nil
}

func (i *Installer) setupRepository(ctx context.Context, client *ssh.Client, config Config) error {
	// Check if repo exists
	checkCmd := fmt.Sprintf("test -d %s/.git", config.RepoPath)
	result, _ := client.ExecSudo(ctx, checkCmd)

	if result.ExitCode != 0 {
		// Clone repository
		cloneCmd := fmt.Sprintf("git clone -b %s %s %s", config.Branch, config.RepoURL, config.RepoPath)
		result, err := client.ExecSudo(ctx, cloneCmd)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("git clone failed: %s", result.Stderr)
		}
	} else {
		// Update repository (wrap in bash because cd is a shell builtin)
		updateCmd := fmt.Sprintf("bash -c 'cd %s && git fetch origin && git reset --hard origin/%s'", config.RepoPath, config.Branch)
		result, err := client.ExecSudo(ctx, updateCmd)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("git update failed: %s", result.Stderr)
		}
	}

	return nil
}

func (i *Installer) installPullScript(ctx context.Context, client *ssh.Client, config Config) error {
	script, err := renderPullScript(config)
	if err != nil {
		return err
	}

	// Base64 encode the script and use bash -c for sudo file writing
	encoded := base64Encode([]byte(script))
	cmd := fmt.Sprintf("bash -c \"echo '%s' | base64 -d > /usr/local/bin/nixfleet-pull && chmod +x /usr/local/bin/nixfleet-pull\"", encoded)
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to install script: %s", result.Stderr)
	}

	return nil
}

// base64Encode encodes data to base64 string
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func (i *Installer) installSystemdUnits(ctx context.Context, client *ssh.Client, config Config) error {
	// Install service unit
	service := renderServiceUnit(config)
	encodedService := base64Encode([]byte(service))
	cmd := fmt.Sprintf("bash -c \"echo '%s' | base64 -d > /etc/systemd/system/nixfleet-pull.service\"", encodedService)
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to install service: %s", result.Stderr)
	}

	// Install timer unit
	timer := renderTimerUnit(config)
	encodedTimer := base64Encode([]byte(timer))
	cmd = fmt.Sprintf("bash -c \"echo '%s' | base64 -d > /etc/systemd/system/nixfleet-pull.timer\"", encodedTimer)
	result, err = client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to install timer: %s", result.Stderr)
	}

	// Reload systemd
	result, err = client.ExecSudo(ctx, "systemctl daemon-reload")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to reload systemd: %s", result.Stderr)
	}

	return nil
}

func (i *Installer) enableTimer(ctx context.Context, client *ssh.Client) error {
	result, err := client.ExecSudo(ctx, "systemctl enable --now nixfleet-pull.timer")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to enable timer: %s", result.Stderr)
	}
	return nil
}

// TriggerPull manually triggers a pull operation
func (i *Installer) TriggerPull(ctx context.Context, client *ssh.Client) error {
	result, err := client.ExecSudo(ctx, "systemctl start nixfleet-pull.service")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to trigger pull: %s", result.Stderr)
	}
	return nil
}

var pullScriptTemplate = `#!/bin/bash
# NixFleet Pull Mode Script
# Generated by nixfleet pull-mode install

set -euo pipefail

REPO_PATH="{{.RepoPath}}"
HOST_NAME="{{.HostName}}"
BRANCH="{{.Branch}}"
LOG_FILE="/var/log/nixfleet/pull.log"
LOCK_FILE="/var/run/nixfleet-pull.lock"
{{if .WebhookURL}}WEBHOOK_URL="{{.WebhookURL}}"{{end}}
{{if .WebhookSecret}}WEBHOOK_SECRET="{{.WebhookSecret}}"{{end}}

log() {
    echo "$(date -Iseconds) $*" | tee -a "$LOG_FILE"
}

notify() {
    local status="$1"
    local message="$2"
    {{if .WebhookURL}}
    local payload="{\"host\":\"$HOST_NAME\",\"status\":\"$status\",\"message\":\"$message\",\"timestamp\":\"$(date -Iseconds)\"}"
    {{if .WebhookSecret}}
    local signature=$(echo -n "$payload" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | awk '{print $2}')
    curl -s -X POST "$WEBHOOK_URL" \
        -H "Content-Type: application/json" \
        -H "X-NixFleet-Signature: sha256=$signature" \
        -d "$payload" || true
    {{else}}
    curl -s -X POST "$WEBHOOK_URL" \
        -H "Content-Type: application/json" \
        -d "$payload" || true
    {{end}}
    {{end}}
}

cleanup() {
    rm -f "$LOCK_FILE"
}
trap cleanup EXIT

# Acquire lock
exec 200>"$LOCK_FILE"
if ! flock -n 200; then
    log "ERROR: Another pull operation is in progress"
    exit 1
fi

log "Starting NixFleet pull for $HOST_NAME"
notify "started" "Pull operation started"

cd "$REPO_PATH"

# Fetch and check for changes
OLD_COMMIT=$(git rev-parse HEAD)
log "Current commit: $OLD_COMMIT"

git fetch origin "$BRANCH" 2>&1 | tee -a "$LOG_FILE"
NEW_COMMIT=$(git rev-parse "origin/$BRANCH")

if [ "$OLD_COMMIT" = "$NEW_COMMIT" ]; then
    log "No changes detected, skipping apply"
    notify "success" "No changes detected"
    exit 0
fi

log "New commit available: $NEW_COMMIT"
git reset --hard "origin/$BRANCH" 2>&1 | tee -a "$LOG_FILE"

# Build and apply configuration
log "Building configuration for $HOST_NAME..."
if ! nix build ".#nixfleetConfigurations.$HOST_NAME.system" --no-link 2>&1 | tee -a "$LOG_FILE"; then
    log "ERROR: Build failed"
    notify "failed" "Build failed for commit $NEW_COMMIT"
    git reset --hard "$OLD_COMMIT"
    exit 1
fi

SYSTEM_PATH=$(nix path-info ".#nixfleetConfigurations.$HOST_NAME.system")
log "System path: $SYSTEM_PATH"

# Activate the configuration
log "Activating configuration..."
if ! "$SYSTEM_PATH/bin/nixfleet-activate" 2>&1 | tee -a "$LOG_FILE"; then
    log "ERROR: Activation failed"
    notify "failed" "Activation failed for commit $NEW_COMMIT"
    exit 1
fi

# Update profile
nix-env --profile /nix/var/nix/profiles/nixfleet/system --set "$SYSTEM_PATH"

log "Successfully applied commit $NEW_COMMIT"
notify "success" "Applied commit $NEW_COMMIT (was $OLD_COMMIT)"

# Run health checks if available
if [ -x "$SYSTEM_PATH/bin/nixfleet-health-check" ]; then
    log "Running health checks..."
    if ! "$SYSTEM_PATH/bin/nixfleet-health-check" 2>&1 | tee -a "$LOG_FILE"; then
        log "WARNING: Health checks failed"
        notify "warning" "Health checks failed after applying $NEW_COMMIT"
    fi
fi

log "Pull operation completed successfully"
`

func renderPullScript(config Config) (string, error) {
	tmpl, err := template.New("pullscript").Parse(pullScriptTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func renderServiceUnit(config Config) string {
	return fmt.Sprintf(`[Unit]
Description=NixFleet Pull Mode - Fetch and apply configuration
After=network-online.target nss-lookup.target
Wants=network-online.target
Documentation=https://github.com/zach-source/nix-fleet

[Service]
Type=oneshot
ExecStart=/usr/local/bin/nixfleet-pull
Environment=HOME=/root
Environment=PATH=/nix/var/nix/profiles/default/bin:/nix/var/nix/profiles/nixfleet/system/bin:/usr/local/bin:/usr/bin:/bin
StandardOutput=journal
StandardError=journal
TimeoutStartSec=600
# Retry on failure
Restart=on-failure
RestartSec=60
StartLimitBurst=3
StartLimitIntervalSec=300

[Install]
WantedBy=multi-user.target
`)
}

func renderTimerUnit(config Config) string {
	onBoot := ""
	if config.ApplyOnBoot {
		onBoot = "OnBootSec=2min"
	}

	return fmt.Sprintf(`[Unit]
Description=NixFleet Pull Mode Timer
Documentation=https://github.com/zach-source/nix-fleet

[Timer]
OnUnitActiveSec=%s
%s
RandomizedDelaySec=30
Persistent=true

[Install]
WantedBy=timers.target
`, config.Interval, onBoot)
}

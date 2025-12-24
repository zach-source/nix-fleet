// Package osupdate implements OS update policies and orchestration for Ubuntu hosts
package osupdate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// Policy represents an OS update policy
type Policy string

const (
	// PolicySecurityDaily applies security updates daily via unattended-upgrades
	PolicySecurityDaily Policy = "security-daily"

	// PolicyFullWeekly applies full updates weekly during maintenance window
	PolicyFullWeekly Policy = "full-weekly"

	// PolicyManual only reports pending updates, no automatic changes
	PolicyManual Policy = "manual"
)

// ParsePolicy parses a policy string
func ParsePolicy(s string) (Policy, error) {
	switch strings.ToLower(s) {
	case "security-daily", "security":
		return PolicySecurityDaily, nil
	case "full-weekly", "full":
		return PolicyFullWeekly, nil
	case "manual", "none":
		return PolicyManual, nil
	default:
		return "", fmt.Errorf("unknown update policy: %s (valid: security-daily, full-weekly, manual)", s)
	}
}

// PolicyConfig contains configuration for an update policy
type PolicyConfig struct {
	Policy            Policy
	MaintenanceWindow string   // e.g., "Sun 02:00-06:00"
	HeldPackages      []string // Packages to hold (apt-mark hold)
	AllowReboot       bool     // Allow automatic reboot if required
	RebootDelay       time.Duration
}

// DefaultPolicyConfig returns sensible defaults for a policy
func DefaultPolicyConfig(policy Policy) PolicyConfig {
	switch policy {
	case PolicySecurityDaily:
		return PolicyConfig{
			Policy:            PolicySecurityDaily,
			MaintenanceWindow: "02:00-06:00", // Daily window
			HeldPackages:      []string{},
			AllowReboot:       false,
			RebootDelay:       0,
		}
	case PolicyFullWeekly:
		return PolicyConfig{
			Policy:            PolicyFullWeekly,
			MaintenanceWindow: "Sun 02:00-06:00", // Weekly on Sunday
			HeldPackages:      []string{},
			AllowReboot:       true,
			RebootDelay:       5 * time.Minute,
		}
	case PolicyManual:
		return PolicyConfig{
			Policy:            PolicyManual,
			MaintenanceWindow: "",
			HeldPackages:      []string{},
			AllowReboot:       false,
			RebootDelay:       0,
		}
	default:
		return PolicyConfig{Policy: PolicyManual}
	}
}

// Updater handles OS updates on remote hosts
type Updater struct{}

// NewUpdater creates a new OS updater
func NewUpdater() *Updater {
	return &Updater{}
}

// ConfigureSecurityDaily configures unattended-upgrades for security updates
func (u *Updater) ConfigureSecurityDaily(ctx context.Context, client *ssh.Client) error {
	// Configure unattended-upgrades for security-only updates
	config := `// Nix-managed unattended-upgrades configuration
// Security updates only - applied daily

Unattended-Upgrade::Allowed-Origins {
    "${distro_id}:${distro_codename}-security";
    "${distro_id}ESMApps:${distro_codename}-apps-security";
    "${distro_id}ESM:${distro_codename}-infra-security";
};

// Don't install recommended packages
Unattended-Upgrade::InstallOnShutdown "false";

// Remove unused automatically installed kernel-related packages
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";

// Remove unused dependencies
Unattended-Upgrade::Remove-Unused-Dependencies "true";

// Don't automatically reboot (NixFleet handles this)
Unattended-Upgrade::Automatic-Reboot "false";

// Email notifications (optional)
// Unattended-Upgrade::Mail "root";

// Only run during specific hours (optional)
// Unattended-Upgrade::OnlyOnACPower "true";

// Logging
Unattended-Upgrade::SyslogEnable "true";
Unattended-Upgrade::SyslogFacility "daemon";
`

	// Write the configuration
	writeCmd := fmt.Sprintf("cat > /etc/apt/apt.conf.d/50unattended-upgrades-nixfleet << 'EOF'\n%s\nEOF", config)
	result, err := client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write unattended-upgrades config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write config: %s", result.Stderr)
	}

	// Configure auto-upgrades to run daily
	autoConfig := `// Enable automatic updates
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`

	writeCmd = fmt.Sprintf("cat > /etc/apt/apt.conf.d/20auto-upgrades-nixfleet << 'EOF'\n%s\nEOF", autoConfig)
	result, err = client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write auto-upgrades config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write auto config: %s", result.Stderr)
	}

	// Enable the unattended-upgrades service
	result, err = client.ExecSudo(ctx, "systemctl enable unattended-upgrades && systemctl start unattended-upgrades")
	if err != nil {
		return fmt.Errorf("failed to enable unattended-upgrades: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to enable service: %s", result.Stderr)
	}

	return nil
}

// ConfigureFullWeekly configures updates for full weekly upgrades
func (u *Updater) ConfigureFullWeekly(ctx context.Context, client *ssh.Client, window string) error {
	// For full-weekly, we use unattended-upgrades with all origins
	config := `// Nix-managed unattended-upgrades configuration
// Full updates - applied weekly

Unattended-Upgrade::Allowed-Origins {
    "${distro_id}:${distro_codename}";
    "${distro_id}:${distro_codename}-security";
    "${distro_id}:${distro_codename}-updates";
    "${distro_id}ESMApps:${distro_codename}-apps-security";
    "${distro_id}ESM:${distro_codename}-infra-security";
};

// Remove unused automatically installed kernel-related packages
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";

// Remove unused dependencies
Unattended-Upgrade::Remove-Unused-Dependencies "true";

// Don't automatically reboot (NixFleet handles this)
Unattended-Upgrade::Automatic-Reboot "false";

// Logging
Unattended-Upgrade::SyslogEnable "true";
Unattended-Upgrade::SyslogFacility "daemon";
`

	writeCmd := fmt.Sprintf("cat > /etc/apt/apt.conf.d/50unattended-upgrades-nixfleet << 'EOF'\n%s\nEOF", config)
	result, err := client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write unattended-upgrades config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write config: %s", result.Stderr)
	}

	// Configure auto-upgrades to run weekly (7 days)
	autoConfig := `// Enable automatic updates (weekly)
APT::Periodic::Update-Package-Lists "7";
APT::Periodic::Unattended-Upgrade "7";
APT::Periodic::AutocleanInterval "7";
`

	writeCmd = fmt.Sprintf("cat > /etc/apt/apt.conf.d/20auto-upgrades-nixfleet << 'EOF'\n%s\nEOF", autoConfig)
	result, err = client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write auto-upgrades config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write auto config: %s", result.Stderr)
	}

	// Enable the unattended-upgrades service
	result, err = client.ExecSudo(ctx, "systemctl enable unattended-upgrades && systemctl start unattended-upgrades")
	if err != nil {
		return fmt.Errorf("failed to enable unattended-upgrades: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to enable service: %s", result.Stderr)
	}

	return nil
}

// ConfigureManual disables automatic updates
func (u *Updater) ConfigureManual(ctx context.Context, client *ssh.Client) error {
	// Disable auto-upgrades
	autoConfig := `// Disable automatic updates (manual policy)
APT::Periodic::Update-Package-Lists "0";
APT::Periodic::Unattended-Upgrade "0";
APT::Periodic::AutocleanInterval "0";
`

	writeCmd := fmt.Sprintf("cat > /etc/apt/apt.conf.d/20auto-upgrades-nixfleet << 'EOF'\n%s\nEOF", autoConfig)
	result, err := client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write auto-upgrades config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write auto config: %s", result.Stderr)
	}

	// Stop unattended-upgrades if running
	_, _ = client.ExecSudo(ctx, "systemctl stop unattended-upgrades 2>/dev/null || true")

	return nil
}

// ConfigurePolicy applies a policy configuration to a host
func (u *Updater) ConfigurePolicy(ctx context.Context, client *ssh.Client, config PolicyConfig) error {
	// First, apply package holds if any
	if len(config.HeldPackages) > 0 {
		if err := u.HoldPackages(ctx, client, config.HeldPackages); err != nil {
			return fmt.Errorf("failed to hold packages: %w", err)
		}
	}

	// Apply the policy
	switch config.Policy {
	case PolicySecurityDaily:
		return u.ConfigureSecurityDaily(ctx, client)
	case PolicyFullWeekly:
		return u.ConfigureFullWeekly(ctx, client, config.MaintenanceWindow)
	case PolicyManual:
		return u.ConfigureManual(ctx, client)
	default:
		return fmt.Errorf("unknown policy: %s", config.Policy)
	}
}

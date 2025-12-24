package osupdate

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// UpdateResult contains the result of an OS update operation
type UpdateResult struct {
	Success         bool
	PackagesUpdated []PackageUpdate
	RebootRequired  bool
	StartTime       time.Time
	EndTime         time.Time
	Stdout          string
	Stderr          string
}

// PackageUpdate represents a single package update
type PackageUpdate struct {
	Name       string
	OldVersion string
	NewVersion string
	Action     string // "upgrade", "install", "remove"
}

// PendingUpdates contains information about available updates
type PendingUpdates struct {
	SecurityUpdates []PendingPackage
	RegularUpdates  []PendingPackage
	TotalCount      int
}

// PendingPackage represents a package with an available update
type PendingPackage struct {
	Name           string
	CurrentVersion string
	NewVersion     string
	IsSecurityFix  bool
}

// RefreshPackageCache runs apt-get update to refresh package lists
func (u *Updater) RefreshPackageCache(ctx context.Context, client *ssh.Client) error {
	result, err := client.ExecSudo(ctx, "apt-get update")
	if err != nil {
		return fmt.Errorf("failed to refresh package cache: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("apt-get update failed: %s", result.Stderr)
	}

	return nil
}

// CheckPendingUpdates returns information about available updates
func (u *Updater) CheckPendingUpdates(ctx context.Context, client *ssh.Client) (*PendingUpdates, error) {
	// First, refresh the cache
	if err := u.RefreshPackageCache(ctx, client); err != nil {
		return nil, err
	}

	pending := &PendingUpdates{}

	// Get list of upgradable packages
	result, err := client.Exec(ctx, "apt list --upgradable 2>/dev/null | grep -v '^Listing'")
	if err != nil {
		return nil, fmt.Errorf("failed to check pending updates: %w", err)
	}

	// Parse output like: package/focal-security 1.2.3-1 amd64 [upgradable from: 1.2.2-1]
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	upgradeRegex := regexp.MustCompile(`^([^/]+)/(\S+)\s+(\S+)\s+\S+\s+\[upgradable from:\s+([^\]]+)\]`)

	for _, line := range lines {
		if line == "" {
			continue
		}

		matches := upgradeRegex.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		pkg := PendingPackage{
			Name:           matches[1],
			NewVersion:     matches[3],
			CurrentVersion: matches[4],
			IsSecurityFix:  strings.Contains(matches[2], "security"),
		}

		if pkg.IsSecurityFix {
			pending.SecurityUpdates = append(pending.SecurityUpdates, pkg)
		} else {
			pending.RegularUpdates = append(pending.RegularUpdates, pkg)
		}
		pending.TotalCount++
	}

	return pending, nil
}

// ApplySecurityUpdates applies only security updates
func (u *Updater) ApplySecurityUpdates(ctx context.Context, client *ssh.Client) (*UpdateResult, error) {
	result := &UpdateResult{
		StartTime: time.Now(),
	}

	// Run unattended-upgrade for security updates only
	execResult, err := client.ExecSudo(ctx, "unattended-upgrade -v 2>&1")
	result.EndTime = time.Now()

	if err != nil {
		result.Stderr = err.Error()
		return result, fmt.Errorf("failed to apply security updates: %w", err)
	}

	result.Stdout = execResult.Stdout
	result.Stderr = execResult.Stderr
	result.Success = execResult.ExitCode == 0

	// Parse updated packages from output
	result.PackagesUpdated = parseUnattendedUpgradeOutput(execResult.Stdout)

	// Check if reboot is required
	result.RebootRequired, _ = u.IsRebootRequired(ctx, client)

	return result, nil
}

// ApplyAllUpdates applies all available updates (security + regular)
func (u *Updater) ApplyAllUpdates(ctx context.Context, client *ssh.Client) (*UpdateResult, error) {
	result := &UpdateResult{
		StartTime: time.Now(),
	}

	// First get list of packages that will be upgraded
	pendingBefore, err := u.CheckPendingUpdates(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to check pending updates: %w", err)
	}

	// Run apt-get upgrade with DEBIAN_FRONTEND=noninteractive
	cmd := "DEBIAN_FRONTEND=noninteractive apt-get upgrade -y -o Dpkg::Options::='--force-confdef' -o Dpkg::Options::='--force-confold'"
	execResult, err := client.ExecSudo(ctx, cmd)
	result.EndTime = time.Now()

	if err != nil {
		result.Stderr = err.Error()
		return result, fmt.Errorf("failed to apply updates: %w", err)
	}

	result.Stdout = execResult.Stdout
	result.Stderr = execResult.Stderr
	result.Success = execResult.ExitCode == 0

	// Build package update list from pending updates
	for _, pkg := range pendingBefore.SecurityUpdates {
		result.PackagesUpdated = append(result.PackagesUpdated, PackageUpdate{
			Name:       pkg.Name,
			OldVersion: pkg.CurrentVersion,
			NewVersion: pkg.NewVersion,
			Action:     "upgrade",
		})
	}
	for _, pkg := range pendingBefore.RegularUpdates {
		result.PackagesUpdated = append(result.PackagesUpdated, PackageUpdate{
			Name:       pkg.Name,
			OldVersion: pkg.CurrentVersion,
			NewVersion: pkg.NewVersion,
			Action:     "upgrade",
		})
	}

	// Check if reboot is required
	result.RebootRequired, _ = u.IsRebootRequired(ctx, client)

	return result, nil
}

// ApplyDistUpgrade applies dist-upgrade (may add/remove packages)
func (u *Updater) ApplyDistUpgrade(ctx context.Context, client *ssh.Client) (*UpdateResult, error) {
	result := &UpdateResult{
		StartTime: time.Now(),
	}

	// Run apt-get dist-upgrade
	cmd := "DEBIAN_FRONTEND=noninteractive apt-get dist-upgrade -y -o Dpkg::Options::='--force-confdef' -o Dpkg::Options::='--force-confold'"
	execResult, err := client.ExecSudo(ctx, cmd)
	result.EndTime = time.Now()

	if err != nil {
		result.Stderr = err.Error()
		return result, fmt.Errorf("failed to apply dist-upgrade: %w", err)
	}

	result.Stdout = execResult.Stdout
	result.Stderr = execResult.Stderr
	result.Success = execResult.ExitCode == 0

	// Parse upgraded packages from apt output
	result.PackagesUpdated = parseAptUpgradeOutput(execResult.Stdout)

	// Check if reboot is required
	result.RebootRequired, _ = u.IsRebootRequired(ctx, client)

	return result, nil
}

// IsRebootRequired checks if a reboot is required after updates
func (u *Updater) IsRebootRequired(ctx context.Context, client *ssh.Client) (bool, error) {
	result, err := client.Exec(ctx, "test -f /var/run/reboot-required && echo 'yes' || echo 'no'")
	if err != nil {
		return false, fmt.Errorf("failed to check reboot status: %w", err)
	}

	return strings.TrimSpace(result.Stdout) == "yes", nil
}

// GetRebootRequiredPackages returns the list of packages that triggered reboot requirement
func (u *Updater) GetRebootRequiredPackages(ctx context.Context, client *ssh.Client) ([]string, error) {
	result, err := client.Exec(ctx, "cat /var/run/reboot-required.pkgs 2>/dev/null || true")
	if err != nil {
		return nil, fmt.Errorf("failed to get reboot required packages: %w", err)
	}

	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		return []string{}, nil
	}

	return strings.Split(output, "\n"), nil
}

// ScheduleReboot schedules a system reboot
func (u *Updater) ScheduleReboot(ctx context.Context, client *ssh.Client, delay time.Duration) error {
	minutes := int(delay.Minutes())
	if minutes < 1 {
		minutes = 1
	}

	cmd := fmt.Sprintf("shutdown -r +%d 'NixFleet scheduled reboot after updates'", minutes)
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to schedule reboot: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("shutdown command failed: %s", result.Stderr)
	}

	return nil
}

// CancelReboot cancels a scheduled reboot
func (u *Updater) CancelReboot(ctx context.Context, client *ssh.Client) error {
	result, err := client.ExecSudo(ctx, "shutdown -c 'NixFleet cancelled scheduled reboot'")
	if err != nil {
		return fmt.Errorf("failed to cancel reboot: %w", err)
	}

	// Exit code 1 is ok if no reboot was scheduled
	if result.ExitCode != 0 && !strings.Contains(result.Stderr, "No scheduled shutdown") {
		return fmt.Errorf("failed to cancel reboot: %s", result.Stderr)
	}

	return nil
}

// Cleanup removes old packages and cleans apt cache
func (u *Updater) Cleanup(ctx context.Context, client *ssh.Client) error {
	// Remove orphaned packages
	result, err := client.ExecSudo(ctx, "apt-get autoremove -y")
	if err != nil {
		return fmt.Errorf("failed to autoremove: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("autoremove failed: %s", result.Stderr)
	}

	// Clean apt cache
	result, err = client.ExecSudo(ctx, "apt-get clean")
	if err != nil {
		return fmt.Errorf("failed to clean apt cache: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("apt-get clean failed: %s", result.Stderr)
	}

	return nil
}

// parseUnattendedUpgradeOutput extracts package updates from unattended-upgrade output
func parseUnattendedUpgradeOutput(output string) []PackageUpdate {
	var updates []PackageUpdate

	// Look for lines like: "Packages that will be upgraded: pkg1 pkg2 pkg3"
	// or "Packages that are upgraded: pkg1 pkg2"
	upgradeRegex := regexp.MustCompile(`(?i)packages.*(?:will be|are)\s+upgraded:\s*(.+)`)
	matches := upgradeRegex.FindStringSubmatch(output)
	if matches != nil {
		pkgs := strings.Fields(matches[1])
		for _, pkg := range pkgs {
			updates = append(updates, PackageUpdate{
				Name:   pkg,
				Action: "upgrade",
			})
		}
	}

	return updates
}

// parseAptUpgradeOutput extracts package updates from apt-get upgrade output
func parseAptUpgradeOutput(output string) []PackageUpdate {
	var updates []PackageUpdate

	// Look for "The following packages will be upgraded:" section
	lines := strings.Split(output, "\n")
	inUpgradeSection := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "The following packages will be upgraded:") {
			inUpgradeSection = true
			continue
		}

		if inUpgradeSection {
			// End of section
			if strings.HasPrefix(line, "The following") || line == "" || strings.Contains(line, "upgraded,") {
				inUpgradeSection = false
				continue
			}

			// Parse package names
			pkgs := strings.Fields(line)
			for _, pkg := range pkgs {
				updates = append(updates, PackageUpdate{
					Name:   pkg,
					Action: "upgrade",
				})
			}
		}
	}

	return updates
}

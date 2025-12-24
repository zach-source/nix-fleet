// Package apt provides APT package management for Ubuntu hosts
package apt

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// Manager handles APT package operations on remote hosts
type Manager struct{}

// NewManager creates a new APT manager
func NewManager() *Manager {
	return &Manager{}
}

// Package represents an installed or available package
type Package struct {
	Name             string `json:"name"`
	InstalledVersion string `json:"installed_version,omitempty"`
	AvailableVersion string `json:"available_version,omitempty"`
	Description      string `json:"description,omitempty"`
	Size             string `json:"size,omitempty"`
	IsSecurityUpdate bool   `json:"is_security_update,omitempty"`
}

// UpdateStatus represents the current update status of a host
type UpdateStatus struct {
	LastCheck       time.Time `json:"last_check"`
	PendingUpdates  int       `json:"pending_updates"`
	SecurityUpdates int       `json:"security_updates"`
	Packages        []Package `json:"packages,omitempty"`
	RebootRequired  bool      `json:"reboot_required"`
}

// CheckUpdates checks for available updates on the host
func (m *Manager) CheckUpdates(ctx context.Context, client *ssh.Client) (*UpdateStatus, error) {
	status := &UpdateStatus{
		LastCheck: time.Now(),
	}

	// Update package lists
	updateResult, err := client.ExecSudo(ctx, "apt-get update -qq 2>/dev/null")
	if err != nil {
		return nil, fmt.Errorf("apt-get update failed: %w", err)
	}
	if updateResult.ExitCode != 0 {
		return nil, fmt.Errorf("apt-get update failed: %s", updateResult.Stderr)
	}

	// Get list of upgradable packages
	listResult, err := client.Exec(ctx, "apt list --upgradable 2>/dev/null | grep -v '^Listing'")
	if err != nil {
		return nil, fmt.Errorf("apt list failed: %w", err)
	}

	// Parse the upgradable packages
	if listResult.ExitCode == 0 && strings.TrimSpace(listResult.Stdout) != "" {
		status.Packages = m.parseUpgradablePackages(listResult.Stdout)
		status.PendingUpdates = len(status.Packages)

		// Count security updates
		for _, pkg := range status.Packages {
			if pkg.IsSecurityUpdate {
				status.SecurityUpdates++
			}
		}
	}

	// Check if reboot is required
	rebootResult, err := client.Exec(ctx, "test -f /var/run/reboot-required && echo yes || echo no")
	if err == nil && rebootResult.ExitCode == 0 {
		status.RebootRequired = strings.TrimSpace(rebootResult.Stdout) == "yes"
	}

	return status, nil
}

// parseUpgradablePackages parses the output of apt list --upgradable
func (m *Manager) parseUpgradablePackages(output string) []Package {
	var packages []Package
	lines := strings.Split(output, "\n")

	// Pattern: package/source version [upgradable from: old_version]
	re := regexp.MustCompile(`^([^/]+)/([^\s]+)\s+([^\s]+)\s+.*\[upgradable from:\s+([^\]]+)\]`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if len(matches) >= 5 {
			pkg := Package{
				Name:             matches[1],
				AvailableVersion: matches[3],
				InstalledVersion: matches[4],
			}

			// Check if it's from a security source
			source := matches[2]
			if strings.Contains(source, "-security") {
				pkg.IsSecurityUpdate = true
			}

			packages = append(packages, pkg)
		}
	}

	return packages
}

// Upgrade performs a system upgrade
func (m *Manager) Upgrade(ctx context.Context, client *ssh.Client, securityOnly bool) (*UpgradeResult, error) {
	result := &UpgradeResult{
		StartTime: time.Now(),
	}

	var cmd string
	if securityOnly {
		// Only install security updates using unattended-upgrades
		cmd = "unattended-upgrade --dry-run -d 2>&1 | grep 'Packages that will be upgraded' || apt-get upgrade -y -o Dir::Etc::SourceList=/etc/apt/sources.list.d/ubuntu-security.list"
	} else {
		cmd = "DEBIAN_FRONTEND=noninteractive apt-get upgrade -y"
	}

	upgradeResult, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result, err
	}

	result.EndTime = time.Now()
	result.Output = upgradeResult.Stdout + upgradeResult.Stderr
	result.Success = upgradeResult.ExitCode == 0

	if !result.Success {
		result.Error = fmt.Sprintf("upgrade failed with exit code %d", upgradeResult.ExitCode)
	}

	// Parse upgraded packages from output
	result.UpgradedPackages = m.parseUpgradedPackages(result.Output)

	return result, nil
}

// UpgradeResult represents the result of an upgrade operation
type UpgradeResult struct {
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
	Success          bool      `json:"success"`
	Error            string    `json:"error,omitempty"`
	Output           string    `json:"output,omitempty"`
	UpgradedPackages []string  `json:"upgraded_packages,omitempty"`
}

// parseUpgradedPackages extracts package names from upgrade output
func (m *Manager) parseUpgradedPackages(output string) []string {
	var packages []string

	// Look for lines like "Unpacking package (version) over (old_version)"
	// or "Setting up package (version)"
	re := regexp.MustCompile(`(?:Unpacking|Setting up)\s+([^\s]+)\s+\(`)

	for _, match := range re.FindAllStringSubmatch(output, -1) {
		if len(match) >= 2 {
			// Avoid duplicates
			found := false
			for _, p := range packages {
				if p == match[1] {
					found = true
					break
				}
			}
			if !found {
				packages = append(packages, match[1])
			}
		}
	}

	return packages
}

// InstallPackage installs a specific package
func (m *Manager) InstallPackage(ctx context.Context, client *ssh.Client, packageName string) error {
	cmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get install -y %s", packageName)
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("install failed: %s", result.Stderr)
	}
	return nil
}

// RemovePackage removes a specific package
func (m *Manager) RemovePackage(ctx context.Context, client *ssh.Client, packageName string) error {
	cmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get remove -y %s", packageName)
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("remove failed: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("remove failed: %s", result.Stderr)
	}
	return nil
}

// GetInstalledPackages returns a list of installed packages
func (m *Manager) GetInstalledPackages(ctx context.Context, client *ssh.Client) ([]Package, error) {
	// Get list of manually installed packages (not dependencies)
	result, err := client.Exec(ctx, "apt-mark showmanual 2>/dev/null")
	if err != nil {
		return nil, fmt.Errorf("getting installed packages: %w", err)
	}

	var packages []Package
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		packages = append(packages, Package{Name: line})
	}

	return packages, nil
}

// GetPackageInfo returns detailed information about a package
func (m *Manager) GetPackageInfo(ctx context.Context, client *ssh.Client, packageName string) (*Package, error) {
	cmd := fmt.Sprintf("dpkg-query -W -f='${Package}|${Version}|${Installed-Size}|${Description}' %s 2>/dev/null", packageName)
	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("getting package info: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("package not found: %s", packageName)
	}

	parts := strings.SplitN(result.Stdout, "|", 4)
	if len(parts) < 4 {
		return nil, fmt.Errorf("unexpected output format")
	}

	pkg := &Package{
		Name:             parts[0],
		InstalledVersion: parts[1],
		Size:             parts[2] + " KB",
		Description:      parts[3],
	}

	return pkg, nil
}

// AutoRemove removes unused packages
func (m *Manager) AutoRemove(ctx context.Context, client *ssh.Client) ([]string, error) {
	// First do a dry-run to see what would be removed
	dryResult, err := client.ExecSudo(ctx, "apt-get autoremove --dry-run 2>/dev/null | grep '^Remv' | awk '{print $2}'")
	if err != nil {
		return nil, fmt.Errorf("autoremove dry-run failed: %w", err)
	}

	var toRemove []string
	for _, line := range strings.Split(dryResult.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			toRemove = append(toRemove, line)
		}
	}

	if len(toRemove) == 0 {
		return nil, nil
	}

	// Actually perform autoremove
	result, err := client.ExecSudo(ctx, "DEBIAN_FRONTEND=noninteractive apt-get autoremove -y")
	if err != nil {
		return nil, fmt.Errorf("autoremove failed: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("autoremove failed: %s", result.Stderr)
	}

	return toRemove, nil
}

// CleanCache cleans the apt package cache
func (m *Manager) CleanCache(ctx context.Context, client *ssh.Client) (int64, error) {
	// Get cache size before
	beforeResult, err := client.Exec(ctx, "du -sb /var/cache/apt/archives 2>/dev/null | awk '{print $1}'")
	if err != nil {
		return 0, fmt.Errorf("checking cache size: %w", err)
	}

	beforeSize, _ := strconv.ParseInt(strings.TrimSpace(beforeResult.Stdout), 10, 64)

	// Clean the cache
	result, err := client.ExecSudo(ctx, "apt-get clean")
	if err != nil {
		return 0, fmt.Errorf("clean failed: %w", err)
	}
	if result.ExitCode != 0 {
		return 0, fmt.Errorf("clean failed: %s", result.Stderr)
	}

	// Get cache size after
	afterResult, err := client.Exec(ctx, "du -sb /var/cache/apt/archives 2>/dev/null | awk '{print $1}'")
	if err != nil {
		return beforeSize, nil
	}

	afterSize, _ := strconv.ParseInt(strings.TrimSpace(afterResult.Stdout), 10, 64)

	return beforeSize - afterSize, nil
}

// GetRebootPackages returns the list of packages that triggered a reboot requirement
func (m *Manager) GetRebootPackages(ctx context.Context, client *ssh.Client) ([]string, error) {
	result, err := client.Exec(ctx, "cat /var/run/reboot-required.pkgs 2>/dev/null")
	if err != nil || result.ExitCode != 0 {
		return nil, nil
	}

	var packages []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			packages = append(packages, line)
		}
	}

	return packages, nil
}

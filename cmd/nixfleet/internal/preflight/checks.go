// Package preflight implements pre-deployment validation checks
package preflight

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// CheckResult represents the result of a preflight check
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// PreflightResults contains all preflight check results for a host
type PreflightResults struct {
	Host    string        `json:"host"`
	Passed  bool          `json:"passed"`
	Checks  []CheckResult `json:"checks"`
	Summary string        `json:"summary"`
}

// Checker performs preflight checks on remote hosts
type Checker struct{}

// NewChecker creates a new preflight checker
func NewChecker() *Checker {
	return &Checker{}
}

// RunAll executes all preflight checks for a host
func (c *Checker) RunAll(ctx context.Context, client *ssh.Client, hostBase string) (*PreflightResults, error) {
	results := &PreflightResults{
		Host:   client.Host(),
		Passed: true,
		Checks: make([]CheckResult, 0),
	}

	// Run checks in order
	checks := []func(context.Context, *ssh.Client) CheckResult{
		c.checkSSHConnectivity,
		c.checkSudoPermissions,
		c.checkDiskSpaceNix,
		c.checkDiskSpaceVar,
	}

	// Add platform-specific checks
	switch hostBase {
	case "ubuntu":
		checks = append(checks, c.checkNixDaemon)
	case "nixos":
		checks = append(checks, c.checkNixStore)
	case "darwin":
		checks = append(checks, c.checkNixDaemonLaunchd)
	}

	for _, check := range checks {
		result := check(ctx, client)
		results.Checks = append(results.Checks, result)
		if !result.Passed {
			results.Passed = false
		}
	}

	// Generate summary
	passed := 0
	failed := 0
	for _, check := range results.Checks {
		if check.Passed {
			passed++
		} else {
			failed++
		}
	}
	results.Summary = fmt.Sprintf("%d/%d checks passed", passed, passed+failed)

	return results, nil
}

// checkSSHConnectivity verifies SSH connection is working
func (c *Checker) checkSSHConnectivity(ctx context.Context, client *ssh.Client) CheckResult {
	result := CheckResult{
		Name: "ssh_connectivity",
	}

	// Simple echo command to verify connection
	output, err := client.Exec(ctx, "echo 'nixfleet-preflight-ok'")
	if err != nil {
		result.Passed = false
		result.Message = "SSH connection failed"
		result.Details = err.Error()
		return result
	}

	if output.ExitCode != 0 || !strings.Contains(output.Stdout, "nixfleet-preflight-ok") {
		result.Passed = false
		result.Message = "SSH command execution failed"
		result.Details = output.Stderr
		return result
	}

	result.Passed = true
	result.Message = "SSH connection established"
	return result
}

// checkSudoPermissions verifies passwordless sudo access
func (c *Checker) checkSudoPermissions(ctx context.Context, client *ssh.Client) CheckResult {
	result := CheckResult{
		Name: "sudo_permissions",
	}

	// Try to run a simple sudo command
	output, err := client.Exec(ctx, "sudo -n true")
	if err != nil {
		result.Passed = false
		result.Message = "Sudo check failed"
		result.Details = err.Error()
		return result
	}

	if output.ExitCode != 0 {
		result.Passed = false
		result.Message = "Passwordless sudo not available"
		result.Details = "User cannot run sudo without password. Configure NOPASSWD in sudoers."
		return result
	}

	result.Passed = true
	result.Message = "Passwordless sudo available"
	return result
}

// checkDiskSpaceNix verifies sufficient disk space in /nix
func (c *Checker) checkDiskSpaceNix(ctx context.Context, client *ssh.Client) CheckResult {
	return c.checkDiskSpace(ctx, client, "/nix", "disk_space_nix", 5) // 5GB minimum
}

// checkDiskSpaceVar verifies sufficient disk space in /var
func (c *Checker) checkDiskSpaceVar(ctx context.Context, client *ssh.Client) CheckResult {
	return c.checkDiskSpace(ctx, client, "/var", "disk_space_var", 1) // 1GB minimum
}

// checkDiskSpace checks available disk space on a path
func (c *Checker) checkDiskSpace(ctx context.Context, client *ssh.Client, path, name string, minGB int) CheckResult {
	result := CheckResult{
		Name: name,
	}

	// Get available space in KB
	cmd := fmt.Sprintf("df -k %s 2>/dev/null | tail -1 | awk '{print $4}'", path)
	output, err := client.Exec(ctx, cmd)
	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("Failed to check disk space on %s", path)
		result.Details = err.Error()
		return result
	}

	if output.ExitCode != 0 {
		result.Passed = false
		result.Message = fmt.Sprintf("Path %s not found or not accessible", path)
		result.Details = output.Stderr
		return result
	}

	// Parse available KB
	availKBStr := strings.TrimSpace(output.Stdout)
	availKB, err := strconv.ParseInt(availKBStr, 10, 64)
	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("Failed to parse disk space for %s", path)
		result.Details = fmt.Sprintf("Got: %s", availKBStr)
		return result
	}

	availGB := availKB / (1024 * 1024)
	minKB := int64(minGB) * 1024 * 1024

	if availKB < minKB {
		result.Passed = false
		result.Message = fmt.Sprintf("Insufficient disk space on %s", path)
		result.Details = fmt.Sprintf("Available: %dGB, Required: %dGB", availGB, minGB)
		return result
	}

	result.Passed = true
	result.Message = fmt.Sprintf("%s has %dGB available", path, availGB)
	return result
}

// checkNixDaemon verifies the Nix daemon is running (Ubuntu)
func (c *Checker) checkNixDaemon(ctx context.Context, client *ssh.Client) CheckResult {
	result := CheckResult{
		Name: "nix_daemon",
	}

	// Check if nix-daemon service is active
	output, err := client.Exec(ctx, "systemctl is-active nix-daemon")
	if err != nil {
		result.Passed = false
		result.Message = "Failed to check Nix daemon status"
		result.Details = err.Error()
		return result
	}

	status := strings.TrimSpace(output.Stdout)
	if status != "active" {
		result.Passed = false
		result.Message = "Nix daemon is not running"
		result.Details = fmt.Sprintf("Status: %s. Run: sudo systemctl start nix-daemon", status)
		return result
	}

	// Also verify nix command works
	output, err = client.Exec(ctx, "nix --version")
	if err != nil || output.ExitCode != 0 {
		result.Passed = false
		result.Message = "Nix command not available"
		result.Details = "Nix daemon is running but nix command failed"
		return result
	}

	result.Passed = true
	result.Message = "Nix daemon is active"
	result.Details = strings.TrimSpace(output.Stdout)
	return result
}

// checkNixStore verifies the Nix store is healthy (NixOS)
func (c *Checker) checkNixStore(ctx context.Context, client *ssh.Client) CheckResult {
	result := CheckResult{
		Name: "nix_store",
	}

	// Check nix store can be queried
	output, err := client.ExecSudo(ctx, "nix-store --verify --check-contents 2>&1 | head -5")
	if err != nil {
		result.Passed = false
		result.Message = "Failed to verify Nix store"
		result.Details = err.Error()
		return result
	}

	// nix-store --verify returns 0 even with warnings, check output
	if strings.Contains(output.Stdout, "error:") {
		result.Passed = false
		result.Message = "Nix store has errors"
		result.Details = output.Stdout
		return result
	}

	// Also check nix command works
	output, err = client.Exec(ctx, "nix --version")
	if err != nil || output.ExitCode != 0 {
		result.Passed = false
		result.Message = "Nix command not available"
		result.Details = "Store verification passed but nix command failed"
		return result
	}

	result.Passed = true
	result.Message = "Nix store is healthy"
	result.Details = strings.TrimSpace(output.Stdout)
	return result
}

// CheckSSH performs only the SSH connectivity check
func (c *Checker) CheckSSH(ctx context.Context, client *ssh.Client) CheckResult {
	return c.checkSSHConnectivity(ctx, client)
}

// CheckSudo performs only the sudo permissions check
func (c *Checker) CheckSudo(ctx context.Context, client *ssh.Client) CheckResult {
	return c.checkSudoPermissions(ctx, client)
}

// CheckDiskSpace performs disk space checks for both /nix and /var
func (c *Checker) CheckDiskSpace(ctx context.Context, client *ssh.Client) []CheckResult {
	return []CheckResult{
		c.checkDiskSpaceNix(ctx, client),
		c.checkDiskSpaceVar(ctx, client),
	}
}

// CheckNixDaemon performs the Nix daemon check (Ubuntu only)
func (c *Checker) CheckNixDaemon(ctx context.Context, client *ssh.Client) CheckResult {
	return c.checkNixDaemon(ctx, client)
}

// CheckNixStore performs the Nix store health check (NixOS only)
func (c *Checker) CheckNixStore(ctx context.Context, client *ssh.Client) CheckResult {
	return c.checkNixStore(ctx, client)
}

// checkNixDaemonLaunchd verifies the Nix daemon is running via launchd (Darwin)
func (c *Checker) checkNixDaemonLaunchd(ctx context.Context, client *ssh.Client) CheckResult {
	result := CheckResult{
		Name: "nix_daemon_launchd",
	}

	// Check if nix-daemon launchd service is loaded and running
	output, err := client.Exec(ctx, "launchctl list org.nixos.nix-daemon 2>/dev/null")
	if err != nil {
		result.Passed = false
		result.Message = "Failed to check Nix daemon launchd status"
		result.Details = err.Error()
		return result
	}

	// launchctl list returns exit code 0 if service exists, and shows PID if running
	// Output format: PID	Status	Label
	// If running: "12345	0	org.nixos.nix-daemon"
	// If not running: "-	0	org.nixos.nix-daemon"
	if output.ExitCode != 0 {
		result.Passed = false
		result.Message = "Nix daemon launchd service not found"
		result.Details = "Install Nix with daemon support: curl -L https://nixos.org/nix/install | sh -s -- --daemon"
		return result
	}

	// Check if the service is actually running (has a PID)
	lines := strings.Split(strings.TrimSpace(output.Stdout), "\n")
	if len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) >= 1 && fields[0] == "-" {
			result.Passed = false
			result.Message = "Nix daemon is not running"
			result.Details = "Service is loaded but not running. Try: sudo launchctl kickstart -k system/org.nixos.nix-daemon"
			return result
		}
	}

	// Verify nix command works
	output, err = client.Exec(ctx, "nix --version")
	if err != nil || output.ExitCode != 0 {
		result.Passed = false
		result.Message = "Nix command not available"
		result.Details = "Nix daemon is running but nix command failed"
		return result
	}

	result.Passed = true
	result.Message = "Nix daemon is running via launchd"
	result.Details = strings.TrimSpace(output.Stdout)
	return result
}

// CheckNixDaemonLaunchd performs the Nix daemon launchd check (Darwin only)
func (c *Checker) CheckNixDaemonLaunchd(ctx context.Context, client *ssh.Client) CheckResult {
	return c.checkNixDaemonLaunchd(ctx, client)
}

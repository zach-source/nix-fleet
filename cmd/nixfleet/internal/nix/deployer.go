package nix

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/ssh"
)

// Deployer handles copying closures and activating on hosts
type Deployer struct {
	evaluator *Evaluator
	nixBin    string
}

// NewDeployer creates a new deployer
func NewDeployer(evaluator *Evaluator) *Deployer {
	return &Deployer{
		evaluator: evaluator,
		nixBin:    evaluator.nixBin,
	}
}

// CopyToHost copies a closure to a remote host
func (d *Deployer) CopyToHost(ctx context.Context, closure *HostClosure, host *inventory.Host) error {
	// Build the SSH URI
	sshURI := fmt.Sprintf("ssh://%s@%s", host.SSHUser, host.Addr)
	if host.SSHPort != 22 {
		sshURI = fmt.Sprintf("ssh://%s@%s:%d", host.SSHUser, host.Addr, host.SSHPort)
	}

	// Run nix copy
	cmd := exec.CommandContext(ctx, d.nixBin, "copy", "--to", sshURI, closure.StorePath)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nix copy failed: %w\nstderr: %s", err, stderr.String())
	}

	return nil
}

// ActivateUbuntu activates a configuration on an Ubuntu host
func (d *Deployer) ActivateUbuntu(ctx context.Context, client *ssh.Client, closure *HostClosure) error {
	// The activation script is part of the closure
	activateScript := fmt.Sprintf("%s/activate", closure.StorePath)

	// Run the activation script with sudo
	result, err := client.ExecSudo(ctx, activateScript)
	if err != nil {
		return fmt.Errorf("activation failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("activation failed with exit code %d: %s", result.ExitCode, result.Stderr)
	}

	return nil
}

// ActivateNixOS activates a configuration on a NixOS host
func (d *Deployer) ActivateNixOS(ctx context.Context, client *ssh.Client, closure *HostClosure, action string) error {
	if action == "" {
		action = "switch"
	}

	// Run switch-to-configuration
	switchCmd := fmt.Sprintf("%s/bin/switch-to-configuration %s", closure.StorePath, action)

	result, err := client.ExecSudo(ctx, switchCmd)
	if err != nil {
		return fmt.Errorf("switch-to-configuration failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("switch-to-configuration failed with exit code %d: %s", result.ExitCode, result.Stderr)
	}

	return nil
}

// ActivateDarwin activates a configuration on a macOS/nix-darwin host
func (d *Deployer) ActivateDarwin(ctx context.Context, client *ssh.Client, closure *HostClosure, action string) error {
	if action == "" {
		action = "switch"
	}

	// nix-darwin uses the same switch-to-configuration pattern as NixOS
	// The activate script is at $closure/activate
	var activateCmd string
	switch action {
	case "switch":
		// Full activation - activate now and set as boot default
		activateCmd = fmt.Sprintf("%s/activate", closure.StorePath)
	case "check":
		// Just check if configuration is valid (dry-run)
		activateCmd = fmt.Sprintf("%s/activate-user --check 2>/dev/null || %s/activate --dry-run 2>/dev/null || echo 'check not supported'", closure.StorePath, closure.StorePath)
	default:
		activateCmd = fmt.Sprintf("%s/activate", closure.StorePath)
	}

	result, err := client.ExecSudo(ctx, activateCmd)
	if err != nil {
		return fmt.Errorf("darwin activation failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("darwin activation failed with exit code %d: %s", result.ExitCode, result.Stderr)
	}

	return nil
}

// GetCurrentGeneration gets the current generation on a host
func (d *Deployer) GetCurrentGeneration(ctx context.Context, client *ssh.Client, base string) (int, string, error) {
	var cmd string
	switch base {
	case "nixos":
		cmd = "readlink /run/current-system"
	case "ubuntu":
		cmd = "readlink /nix/var/nix/profiles/nixfleet/system"
	case "darwin":
		cmd = "readlink /run/current-system"
	default:
		return 0, "", fmt.Errorf("unknown base: %s", base)
	}

	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return 0, "", err
	}

	if result.ExitCode != 0 {
		return 0, "", fmt.Errorf("failed to get current generation: %s", result.Stderr)
	}

	storePath := strings.TrimSpace(result.Stdout)
	return 0, storePath, nil // TODO: parse generation number
}

// Rollback rolls back to a previous generation
func (d *Deployer) Rollback(ctx context.Context, client *ssh.Client, base string, generation int) error {
	switch base {
	case "nixos":
		return d.rollbackNixOS(ctx, client, generation)
	case "ubuntu":
		return d.rollbackUbuntu(ctx, client, generation)
	case "darwin":
		return d.rollbackDarwin(ctx, client, generation)
	default:
		return fmt.Errorf("unknown base: %s", base)
	}
}

func (d *Deployer) rollbackNixOS(ctx context.Context, client *ssh.Client, generation int) error {
	var cmd string
	if generation == 0 {
		// Rollback to previous
		cmd = "/run/current-system/bin/switch-to-configuration boot && " +
			"nix-env --profile /nix/var/nix/profiles/system --rollback && " +
			"/nix/var/nix/profiles/system/bin/switch-to-configuration switch"
	} else {
		// Rollback to specific generation
		cmd = fmt.Sprintf("/nix/var/nix/profiles/system-%d-link/bin/switch-to-configuration switch", generation)
	}

	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("rollback failed: %s", result.Stderr)
	}

	return nil
}

func (d *Deployer) rollbackUbuntu(ctx context.Context, client *ssh.Client, generation int) error {
	var cmd string
	if generation == 0 {
		// Rollback to previous
		cmd = "nix-env --profile /nix/var/nix/profiles/nixfleet/system --rollback && " +
			"/nix/var/nix/profiles/nixfleet/system/activate"
	} else {
		// Rollback to specific generation
		cmd = fmt.Sprintf("/nix/var/nix/profiles/nixfleet/system-%d-link/activate", generation)
	}

	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("rollback failed: %s", result.Stderr)
	}

	return nil
}

func (d *Deployer) rollbackDarwin(ctx context.Context, client *ssh.Client, generation int) error {
	var cmd string
	if generation == 0 {
		// Rollback to previous generation
		// nix-darwin stores profiles in /nix/var/nix/profiles/system
		cmd = "nix-env --profile /nix/var/nix/profiles/system --rollback && " +
			"/nix/var/nix/profiles/system/activate"
	} else {
		// Rollback to specific generation
		cmd = fmt.Sprintf("/nix/var/nix/profiles/system-%d-link/activate", generation)
	}

	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("darwin rollback failed: %s", result.Stderr)
	}

	return nil
}

// CheckRebootNeeded checks if a host needs to be rebooted
func (d *Deployer) CheckRebootNeeded(ctx context.Context, client *ssh.Client, base string) (bool, error) {
	switch base {
	case "ubuntu":
		result, err := client.Exec(ctx, "test -f /var/run/reboot-required && echo yes || echo no")
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(result.Stdout) == "yes", nil

	case "nixos":
		// Compare running kernel to booted kernel
		result, err := client.Exec(ctx, `
			running=$(readlink /run/current-system/kernel)
			booted=$(readlink /run/booted-system/kernel 2>/dev/null || echo "")
			if [ "$running" != "$booted" ] && [ -n "$booted" ]; then
				echo yes
			else
				echo no
			fi
		`)
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(result.Stdout) == "yes", nil

	case "darwin":
		// macOS rarely requires reboots for nix-darwin changes
		// Check if a software update is pending that requires restart
		result, err := client.Exec(ctx, `
			if softwareupdate -l 2>/dev/null | grep -q "restart"; then
				echo yes
			else
				echo no
			fi
		`)
		if err != nil {
			// If softwareupdate fails, assume no reboot needed
			return false, nil
		}
		return strings.TrimSpace(result.Stdout) == "yes", nil

	default:
		return false, fmt.Errorf("unknown base: %s", base)
	}
}

// RebootHost reboots a remote host and waits for it to come back
func (d *Deployer) RebootHost(ctx context.Context, client *ssh.Client) error {
	// Schedule reboot in 1 second to allow SSH to close cleanly
	_, err := client.ExecSudo(ctx, "shutdown -r +0")
	return err
}

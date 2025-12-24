package osupdate

import (
	"context"
	"fmt"
	"strings"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// HoldPackages marks packages as held (won't be upgraded)
func (u *Updater) HoldPackages(ctx context.Context, client *ssh.Client, packages []string) error {
	if len(packages) == 0 {
		return nil
	}

	// apt-mark hold package1 package2 ...
	cmd := fmt.Sprintf("apt-mark hold %s", strings.Join(packages, " "))
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to hold packages: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("apt-mark hold failed: %s", result.Stderr)
	}

	return nil
}

// UnholdPackages removes hold from packages
func (u *Updater) UnholdPackages(ctx context.Context, client *ssh.Client, packages []string) error {
	if len(packages) == 0 {
		return nil
	}

	cmd := fmt.Sprintf("apt-mark unhold %s", strings.Join(packages, " "))
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to unhold packages: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("apt-mark unhold failed: %s", result.Stderr)
	}

	return nil
}

// ListHeldPackages returns a list of held packages
func (u *Updater) ListHeldPackages(ctx context.Context, client *ssh.Client) ([]string, error) {
	result, err := client.Exec(ctx, "apt-mark showhold")
	if err != nil {
		return nil, fmt.Errorf("failed to list held packages: %w", err)
	}

	if result.ExitCode != 0 {
		return nil, fmt.Errorf("apt-mark showhold failed: %s", result.Stderr)
	}

	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		return []string{}, nil
	}

	return strings.Split(output, "\n"), nil
}

// PinPackageVersion pins a package to a specific version
func (u *Updater) PinPackageVersion(ctx context.Context, client *ssh.Client, pkg, version string) error {
	pinConfig := fmt.Sprintf(`Package: %s
Pin: version %s
Pin-Priority: 1001
`, pkg, version)

	// Write pin file
	pinFile := fmt.Sprintf("/etc/apt/preferences.d/nixfleet-%s", pkg)
	cmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", pinFile, pinConfig)

	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to pin package %s: %w", pkg, err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write pin file: %s", result.Stderr)
	}

	return nil
}

// UnpinPackage removes a package pin
func (u *Updater) UnpinPackage(ctx context.Context, client *ssh.Client, pkg string) error {
	pinFile := fmt.Sprintf("/etc/apt/preferences.d/nixfleet-%s", pkg)
	cmd := fmt.Sprintf("rm -f %s", pinFile)

	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to unpin package %s: %w", pkg, err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("failed to remove pin file: %s", result.Stderr)
	}

	return nil
}

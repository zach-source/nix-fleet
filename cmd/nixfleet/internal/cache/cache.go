// Package cache implements binary cache and signing support for NixFleet
package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// CacheType represents a binary cache backend
type CacheType string

const (
	CacheTypeS3     CacheType = "s3"
	CacheTypeCachix CacheType = "cachix"
	CacheTypeLocal  CacheType = "local"
	CacheTypeSSH    CacheType = "ssh"
)

// CacheConfig holds binary cache configuration
type CacheConfig struct {
	Type       CacheType
	URL        string // Cache URL (s3://bucket, https://cache.nixos.org, etc.)
	PublicKeys []string
	SecretKey  string // Path to secret signing key
	Priority   int    // Cache priority (lower = preferred)
}

// SigningConfig holds signing key configuration
type SigningConfig struct {
	PublicKey string // Public key content or path
	SecretKey string // Path to secret key file
	KeyName   string // Key name for identification
}

// Manager handles binary cache operations
type Manager struct {
	caches  []CacheConfig
	signing *SigningConfig
}

// NewManager creates a new cache manager
func NewManager(caches []CacheConfig, signing *SigningConfig) *Manager {
	return &Manager{
		caches:  caches,
		signing: signing,
	}
}

// PushToCache pushes a store path to the configured cache
func (m *Manager) PushToCache(ctx context.Context, storePath string, cacheURL string) error {
	if m.signing == nil || m.signing.SecretKey == "" {
		return fmt.Errorf("signing key required to push to cache")
	}

	// Verify secret key exists
	if _, err := os.Stat(m.signing.SecretKey); err != nil {
		return fmt.Errorf("signing key not found: %s", m.signing.SecretKey)
	}

	// Build nix copy command with signing
	args := []string{
		"nix", "copy",
		"--to", cacheURL,
		storePath,
	}

	// Add signing key
	args = append(args, "--secret-key-files", m.signing.SecretKey)

	cmd := strings.Join(args, " ")

	// Execute locally (assumes nix is available on control machine)
	result := execLocalCommand(ctx, cmd)
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to push to cache: %s", result.Stderr)
	}

	return nil
}

// ConfigureHostCache configures a remote host to use the binary caches
func (m *Manager) ConfigureHostCache(ctx context.Context, client *ssh.Client, base string) error {
	switch base {
	case "ubuntu":
		return m.configureUbuntuCache(ctx, client)
	case "nixos":
		return m.configureNixOSCache(ctx, client)
	case "darwin":
		return m.configureDarwinCache(ctx, client)
	default:
		return fmt.Errorf("unsupported base: %s", base)
	}
}

// configureUbuntuCache configures Nix daemon on Ubuntu to use caches
func (m *Manager) configureUbuntuCache(ctx context.Context, client *ssh.Client) error {
	// Build substituters and trusted-public-keys lists
	var substituters []string
	var publicKeys []string

	for _, cache := range m.caches {
		substituters = append(substituters, cache.URL)
		publicKeys = append(publicKeys, cache.PublicKeys...)
	}

	// Add default cache.nixos.org if not present
	hasDefault := false
	for _, s := range substituters {
		if strings.Contains(s, "cache.nixos.org") {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		substituters = append([]string{"https://cache.nixos.org"}, substituters...)
		publicKeys = append([]string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="}, publicKeys...)
	}

	// Write nix.conf snippet
	nixConf := fmt.Sprintf(`# NixFleet managed cache configuration
substituters = %s
trusted-public-keys = %s
`, strings.Join(substituters, " "), strings.Join(publicKeys, " "))

	writeCmd := fmt.Sprintf("mkdir -p /etc/nix/nix.conf.d && cat > /etc/nix/nix.conf.d/nixfleet-cache.conf << 'EOF'\n%s\nEOF", nixConf)
	result, err := client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write cache config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write cache config: %s", result.Stderr)
	}

	// Restart nix-daemon to pick up new config
	result, err = client.ExecSudo(ctx, "systemctl restart nix-daemon")
	if err != nil {
		return fmt.Errorf("failed to restart nix-daemon: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to restart nix-daemon: %s", result.Stderr)
	}

	return nil
}

// configureNixOSCache returns the Nix configuration for NixOS
// Note: For NixOS, cache config should be in the nixosConfiguration
func (m *Manager) configureNixOSCache(ctx context.Context, client *ssh.Client) error {
	// NixOS cache config is typically managed via nix.settings in configuration.nix
	// We'll write a supplementary config file that gets included
	var substituters []string
	var publicKeys []string

	for _, cache := range m.caches {
		substituters = append(substituters, cache.URL)
		publicKeys = append(publicKeys, cache.PublicKeys...)
	}

	nixConf := fmt.Sprintf(`# NixFleet managed cache configuration
substituters = %s
trusted-public-keys = %s
`, strings.Join(substituters, " "), strings.Join(publicKeys, " "))

	writeCmd := fmt.Sprintf("mkdir -p /etc/nix && cat > /etc/nix/nixfleet-cache.conf << 'EOF'\n%s\nEOF", nixConf)
	result, err := client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write cache config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write cache config: %s", result.Stderr)
	}

	return nil
}

// configureDarwinCache configures nix-darwin to use caches
func (m *Manager) configureDarwinCache(ctx context.Context, client *ssh.Client) error {
	var substituters []string
	var publicKeys []string

	for _, cache := range m.caches {
		substituters = append(substituters, cache.URL)
		publicKeys = append(publicKeys, cache.PublicKeys...)
	}

	nixConf := fmt.Sprintf(`# NixFleet managed cache configuration
substituters = %s
trusted-public-keys = %s
`, strings.Join(substituters, " "), strings.Join(publicKeys, " "))

	writeCmd := fmt.Sprintf("mkdir -p /etc/nix && cat > /etc/nix/nixfleet-cache.conf << 'EOF'\n%s\nEOF", nixConf)
	result, err := client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write cache config: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write cache config: %s", result.Stderr)
	}

	// Restart nix-daemon via launchctl
	_, _ = client.ExecSudo(ctx, "launchctl kickstart -k system/org.nixos.nix-daemon")

	return nil
}

// GenerateSigningKey generates a new signing key pair
func GenerateSigningKey(ctx context.Context, keyName string, outputDir string) (*SigningConfig, error) {
	secretPath := filepath.Join(outputDir, keyName+".sec")
	publicPath := filepath.Join(outputDir, keyName+".pub")

	// Generate key using nix key generate-secret
	cmd := fmt.Sprintf("nix key generate-secret --key-name %s > %s", keyName, secretPath)
	result := execLocalCommand(ctx, cmd)
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("failed to generate secret key: %s", result.Stderr)
	}

	// Convert to public key
	cmd = fmt.Sprintf("nix key convert-secret-to-public < %s > %s", secretPath, publicPath)
	result = execLocalCommand(ctx, cmd)
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("failed to generate public key: %s", result.Stderr)
	}

	// Read public key content
	pubKeyBytes, err := os.ReadFile(publicPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read public key: %w", err)
	}

	return &SigningConfig{
		KeyName:   keyName,
		SecretKey: secretPath,
		PublicKey: strings.TrimSpace(string(pubKeyBytes)),
	}, nil
}

// VerifySignature verifies a store path is signed with a trusted key
func (m *Manager) VerifySignature(ctx context.Context, client *ssh.Client, storePath string) (bool, error) {
	// Get signatures for the store path
	cmd := fmt.Sprintf("nix path-info --sigs %s 2>/dev/null || true", storePath)
	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return false, fmt.Errorf("failed to get signatures: %w", err)
	}

	// Check if any trusted key signed it
	for _, cache := range m.caches {
		for _, pubKey := range cache.PublicKeys {
			keyName := strings.Split(pubKey, ":")[0]
			if strings.Contains(result.Stdout, keyName) {
				return true, nil
			}
		}
	}

	return false, nil
}

// LocalCommandResult holds result of local command execution
type LocalCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// execLocalCommand executes a command locally
func execLocalCommand(ctx context.Context, cmdStr string) LocalCommandResult {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := LocalCommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
			result.Stderr = err.Error()
		}
	}

	return result
}

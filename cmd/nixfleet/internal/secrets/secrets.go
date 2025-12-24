// Package secrets implements encrypted secrets management for NixFleet
package secrets

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

// EncryptionType represents the encryption backend
type EncryptionType string

const (
	EncryptionAge  EncryptionType = "age"
	EncryptionSops EncryptionType = "sops"
)

// SecretConfig holds configuration for a secret
type SecretConfig struct {
	Name         string
	SourcePath   string   // Path to encrypted secret file
	DestPath     string   // Target path on host (e.g., /run/nixfleet-secrets/mykey)
	Owner        string   // File owner (default: root)
	Group        string   // File group (default: root)
	Mode         string   // File mode (default: 0400)
	RestartUnits []string // Units to restart when secret changes
}

// Manager handles secrets encryption/decryption
type Manager struct {
	encType    EncryptionType
	identities []string // age identities or sops key paths
	recipients []string // age recipients for encryption
}

// NewManager creates a new secrets manager
func NewManager(encType EncryptionType, identities, recipients []string) *Manager {
	return &Manager{
		encType:    encType,
		identities: identities,
		recipients: recipients,
	}
}

// DecryptSecret decrypts a secret file and returns its contents
func (m *Manager) DecryptSecret(ctx context.Context, encryptedPath string) ([]byte, error) {
	switch m.encType {
	case EncryptionAge:
		return m.decryptAge(ctx, encryptedPath)
	case EncryptionSops:
		return m.decryptSops(ctx, encryptedPath)
	default:
		return nil, fmt.Errorf("unsupported encryption type: %s", m.encType)
	}
}

// decryptAge decrypts using age
func (m *Manager) decryptAge(ctx context.Context, encryptedPath string) ([]byte, error) {
	args := []string{"--decrypt"}

	// Add identity files
	for _, id := range m.identities {
		args = append(args, "-i", id)
	}

	args = append(args, encryptedPath)

	cmd := exec.CommandContext(ctx, "age", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("age decrypt failed: %s", stderr.String())
	}

	return stdout.Bytes(), nil
}

// decryptSops decrypts using sops
func (m *Manager) decryptSops(ctx context.Context, encryptedPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sops", "--decrypt", encryptedPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sops decrypt failed: %s", stderr.String())
	}

	return stdout.Bytes(), nil
}

// EncryptSecret encrypts data and writes to a file
func (m *Manager) EncryptSecret(ctx context.Context, data []byte, outputPath string) error {
	switch m.encType {
	case EncryptionAge:
		return m.encryptAge(ctx, data, outputPath)
	case EncryptionSops:
		return m.encryptSops(ctx, data, outputPath)
	default:
		return fmt.Errorf("unsupported encryption type: %s", m.encType)
	}
}

// encryptAge encrypts using age
func (m *Manager) encryptAge(ctx context.Context, data []byte, outputPath string) error {
	args := []string{"--encrypt", "--armor", "-o", outputPath}

	// Add recipients
	for _, r := range m.recipients {
		args = append(args, "-r", r)
	}

	cmd := exec.CommandContext(ctx, "age", args...)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("age encrypt failed: %s", stderr.String())
	}

	return nil
}

// encryptSops encrypts using sops
func (m *Manager) encryptSops(ctx context.Context, data []byte, outputPath string) error {
	// Write plaintext to temp file
	tmpFile, err := os.CreateTemp("", "nixfleet-secret-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	cmd := exec.CommandContext(ctx, "sops", "--encrypt", "--output", outputPath, tmpFile.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sops encrypt failed: %s", stderr.String())
	}

	return nil
}

// DeploySecret decrypts and deploys a secret to a host
func (m *Manager) DeploySecret(ctx context.Context, client *ssh.Client, secret SecretConfig) error {
	// Decrypt locally
	plaintext, err := m.DecryptSecret(ctx, secret.SourcePath)
	if err != nil {
		return fmt.Errorf("decrypting secret %s: %w", secret.Name, err)
	}

	// Set defaults
	if secret.Owner == "" {
		secret.Owner = "root"
	}
	if secret.Group == "" {
		secret.Group = "root"
	}
	if secret.Mode == "" {
		secret.Mode = "0400"
	}

	// Ensure secrets directory exists
	secretsDir := filepath.Dir(secret.DestPath)
	mkdirCmd := fmt.Sprintf("mkdir -p %s && chmod 0750 %s", secretsDir, secretsDir)
	result, err := client.ExecSudo(ctx, mkdirCmd)
	if err != nil {
		return fmt.Errorf("creating secrets directory: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("creating secrets directory: %s", result.Stderr)
	}

	// Write secret to host via SSH
	// Use base64 to safely transfer binary data
	encoded := base64Encode(plaintext)
	writeCmd := fmt.Sprintf("echo '%s' | base64 -d > %s", encoded, secret.DestPath)
	result, err = client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("writing secret: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("writing secret: %s", result.Stderr)
	}

	// Set ownership and permissions
	chownCmd := fmt.Sprintf("chown %s:%s %s && chmod %s %s",
		secret.Owner, secret.Group, secret.DestPath, secret.Mode, secret.DestPath)
	result, err = client.ExecSudo(ctx, chownCmd)
	if err != nil {
		return fmt.Errorf("setting secret permissions: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("setting secret permissions: %s", result.Stderr)
	}

	return nil
}

// DeploySecrets deploys multiple secrets and handles unit restarts
func (m *Manager) DeploySecrets(ctx context.Context, client *ssh.Client, secrets []SecretConfig) ([]string, error) {
	var unitsToRestart []string
	seenUnits := make(map[string]bool)

	for _, secret := range secrets {
		if err := m.DeploySecret(ctx, client, secret); err != nil {
			return nil, fmt.Errorf("deploying secret %s: %w", secret.Name, err)
		}

		// Collect units to restart
		for _, unit := range secret.RestartUnits {
			if !seenUnits[unit] {
				unitsToRestart = append(unitsToRestart, unit)
				seenUnits[unit] = true
			}
		}
	}

	return unitsToRestart, nil
}

// RestartUnits restarts the specified systemd units
func RestartUnits(ctx context.Context, client *ssh.Client, units []string) error {
	if len(units) == 0 {
		return nil
	}

	cmd := fmt.Sprintf("systemctl restart %s", strings.Join(units, " "))
	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("restarting units: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("restarting units: %s", result.Stderr)
	}

	return nil
}

// GetSecretHash returns a hash of the secret content for change detection
func (m *Manager) GetSecretHash(ctx context.Context, encryptedPath string) (string, error) {
	plaintext, err := m.DecryptSecret(ctx, encryptedPath)
	if err != nil {
		return "", err
	}

	// Use SHA256 for hashing
	cmd := exec.CommandContext(ctx, "shasum", "-a", "256")
	cmd.Stdin = bytes.NewReader(plaintext)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("hashing secret: %w", err)
	}

	// Extract just the hash (first field)
	hash := strings.Fields(stdout.String())[0]
	return hash, nil
}

// CheckSecretChanged checks if a deployed secret differs from source
func (m *Manager) CheckSecretChanged(ctx context.Context, client *ssh.Client, secret SecretConfig) (bool, error) {
	// Get local hash
	localHash, err := m.GetSecretHash(ctx, secret.SourcePath)
	if err != nil {
		return false, fmt.Errorf("getting local hash: %w", err)
	}

	// Get remote hash
	cmd := fmt.Sprintf("shasum -a 256 %s 2>/dev/null || echo 'missing'", secret.DestPath)
	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return true, nil // Assume changed if we can't check
	}

	if strings.Contains(result.Stdout, "missing") {
		return true, nil // Secret doesn't exist on host
	}

	remoteHash := strings.Fields(result.Stdout)[0]
	return localHash != remoteHash, nil
}

// base64Encode encodes data to base64 string
func base64Encode(data []byte) string {
	cmd := exec.Command("base64")
	cmd.Stdin = bytes.NewReader(data)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run()
	return strings.TrimSpace(stdout.String())
}

// GenerateAgeKey generates a new age key pair
func GenerateAgeKey(ctx context.Context, outputPath string) (publicKey string, err error) {
	cmd := exec.CommandContext(ctx, "age-keygen", "-o", outputPath)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("generating age key: %w", err)
	}

	// Extract public key from output
	output := stdout.String()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Public key:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Public key:")), nil
		}
		if strings.HasPrefix(line, "age1") {
			return strings.TrimSpace(line), nil
		}
	}

	// Try to read from file comment
	content, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("reading key file: %w", err)
	}

	for _, line := range strings.Split(string(content), "\n") {
		if strings.Contains(line, "age1") {
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasPrefix(part, "age1") {
					return part, nil
				}
			}
		}
	}

	return "", fmt.Errorf("could not extract public key")
}

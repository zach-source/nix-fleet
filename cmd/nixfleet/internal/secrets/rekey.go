// Package secrets implements encrypted secrets management for NixFleet
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SecretsNixConfig represents the parsed secrets.nix configuration
type SecretsNixConfig struct {
	Admins    map[string]string         `json:"admins"`
	Hosts     map[string]string         `json:"hosts"`
	AllAdmins []string                  `json:"allAdmins"`
	AllHosts  []string                  `json:"allHosts"`
	Secrets   map[string]SecretNixEntry `json:"secrets"`
}

// SecretNixEntry represents a secret entry in secrets.nix
type SecretNixEntry struct {
	PublicKeys []string `json:"publicKeys"`
}

// ParseSecretsNix parses a secrets.nix file and returns the configuration
func ParseSecretsNix(ctx context.Context, secretsNixPath string) (*SecretsNixConfig, error) {
	// Use nix eval to parse the secrets.nix file
	absPath, err := filepath.Abs(secretsNixPath)
	if err != nil {
		return nil, fmt.Errorf("getting absolute path: %w", err)
	}

	// Evaluate the secrets.nix file as JSON
	cmd := exec.CommandContext(ctx, "nix", "eval", "--json", "--file", absPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nix eval failed: %s", stderr.String())
	}

	var config SecretsNixConfig
	if err := json.Unmarshal(stdout.Bytes(), &config); err != nil {
		return nil, fmt.Errorf("parsing nix output: %w", err)
	}

	return &config, nil
}

// RekeySecret re-encrypts a single secret with the specified recipients
func RekeySecret(ctx context.Context, secretPath string, recipients []string, identityPath string) error {
	// Decrypt the secret
	args := []string{"--decrypt", "-i", identityPath, secretPath}
	decryptCmd := exec.CommandContext(ctx, "age", args...)
	var plaintext, stderr bytes.Buffer
	decryptCmd.Stdout = &plaintext
	decryptCmd.Stderr = &stderr

	if err := decryptCmd.Run(); err != nil {
		return fmt.Errorf("decrypting %s: %s", secretPath, stderr.String())
	}

	// Build encryption args
	encryptArgs := []string{"--encrypt", "--armor"}
	for _, r := range recipients {
		encryptArgs = append(encryptArgs, "-r", r)
	}
	encryptArgs = append(encryptArgs, "-o", secretPath)

	encryptCmd := exec.CommandContext(ctx, "age", encryptArgs...)
	encryptCmd.Stdin = bytes.NewReader(plaintext.Bytes())
	stderr.Reset()
	encryptCmd.Stderr = &stderr

	if err := encryptCmd.Run(); err != nil {
		return fmt.Errorf("encrypting %s: %s", secretPath, stderr.String())
	}

	return nil
}

// RekeyAll re-encrypts all secrets based on secrets.nix configuration
func RekeyAll(ctx context.Context, secretsDir string, config *SecretsNixConfig, identityPath string, dryRun bool) ([]string, error) {
	var rekeyed []string

	for secretName, entry := range config.Secrets {
		secretPath := filepath.Join(secretsDir, secretName)

		// Check if file exists
		if _, err := os.Stat(secretPath); os.IsNotExist(err) {
			continue // Skip missing files
		}

		if dryRun {
			rekeyed = append(rekeyed, secretName)
			continue
		}

		if err := RekeySecret(ctx, secretPath, entry.PublicKeys, identityPath); err != nil {
			return rekeyed, fmt.Errorf("rekeying %s: %w", secretName, err)
		}

		rekeyed = append(rekeyed, secretName)
	}

	return rekeyed, nil
}

// EditSecret opens a secret in $EDITOR for editing, then re-encrypts
func EditSecret(ctx context.Context, secretPath string, recipients []string, identityPath string) error {
	// Decrypt to temp file
	tmpFile, err := os.CreateTemp("", "nixfleet-secret-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Decrypt
	args := []string{"--decrypt", "-i", identityPath, "-o", tmpPath, secretPath}
	decryptCmd := exec.CommandContext(ctx, "age", args...)
	var stderr bytes.Buffer
	decryptCmd.Stderr = &stderr

	if err := decryptCmd.Run(); err != nil {
		return fmt.Errorf("decrypting: %s", stderr.String())
	}

	// Get file hash before editing
	beforeHash, err := hashFile(tmpPath)
	if err != nil {
		return fmt.Errorf("hashing before edit: %w", err)
	}

	// Open in editor
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	editorCmd := exec.CommandContext(ctx, editor, tmpPath)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr

	if err := editorCmd.Run(); err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	// Check if file changed
	afterHash, err := hashFile(tmpPath)
	if err != nil {
		return fmt.Errorf("hashing after edit: %w", err)
	}

	if beforeHash == afterHash {
		return nil // No changes
	}

	// Read edited content
	content, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("reading edited content: %w", err)
	}

	// Re-encrypt
	encryptArgs := []string{"--encrypt", "--armor"}
	for _, r := range recipients {
		encryptArgs = append(encryptArgs, "-r", r)
	}
	encryptArgs = append(encryptArgs, "-o", secretPath)

	encryptCmd := exec.CommandContext(ctx, "age", encryptArgs...)
	encryptCmd.Stdin = bytes.NewReader(content)
	stderr.Reset()
	encryptCmd.Stderr = &stderr

	if err := encryptCmd.Run(); err != nil {
		return fmt.Errorf("encrypting: %s", stderr.String())
	}

	return nil
}

// AddSecret creates a new encrypted secret
func AddSecret(ctx context.Context, secretPath string, content []byte, recipients []string) error {
	// Encrypt
	encryptArgs := []string{"--encrypt", "--armor"}
	for _, r := range recipients {
		encryptArgs = append(encryptArgs, "-r", r)
	}
	encryptArgs = append(encryptArgs, "-o", secretPath)

	encryptCmd := exec.CommandContext(ctx, "age", encryptArgs...)
	encryptCmd.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	encryptCmd.Stderr = &stderr

	if err := encryptCmd.Run(); err != nil {
		return fmt.Errorf("encrypting: %s", stderr.String())
	}

	return nil
}

// GetHostAgeKey derives the age public key from an SSH host key
func GetHostAgeKey(ctx context.Context, sshPubKeyPath string) (string, error) {
	// Read the SSH public key
	pubKey, err := os.ReadFile(sshPubKeyPath)
	if err != nil {
		return "", fmt.Errorf("reading SSH key: %w", err)
	}

	// Convert using ssh-to-age
	cmd := exec.CommandContext(ctx, "ssh-to-age")
	cmd.Stdin = bytes.NewReader(pubKey)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh-to-age failed: %s", stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// GetHostAgeKeyFromRemote gets the age public key from a remote host's SSH key
func GetHostAgeKeyFromRemote(ctx context.Context, host, user string, port int) (string, error) {
	if port == 0 {
		port = 22
	}

	// SSH to host and get the public key
	sshArgs := []string{
		"-p", fmt.Sprintf("%d", port),
		fmt.Sprintf("%s@%s", user, host),
		"cat /etc/ssh/ssh_host_ed25519_key.pub",
	}

	sshCmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	sshCmd.Stdout = &stdout
	sshCmd.Stderr = &stderr

	if err := sshCmd.Run(); err != nil {
		return "", fmt.Errorf("SSH failed: %s", stderr.String())
	}

	// Convert to age key
	cmd := exec.CommandContext(ctx, "ssh-to-age")
	cmd.Stdin = bytes.NewReader(stdout.Bytes())
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh-to-age failed: %s", stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// hashFile computes SHA256 hash of a file
func hashFile(path string) (string, error) {
	cmd := exec.Command("shasum", "-a", "256", path)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.Fields(stdout.String())[0], nil
}

// LookupRecipientsForSecret returns the recipients for a secret from secrets.nix
func (c *SecretsNixConfig) LookupRecipientsForSecret(secretName string) ([]string, error) {
	entry, ok := c.Secrets[secretName]
	if !ok {
		return nil, fmt.Errorf("secret %q not found in secrets.nix", secretName)
	}
	return entry.PublicKeys, nil
}

// GetDefaultRecipients returns all admins as default recipients for new secrets
func (c *SecretsNixConfig) GetDefaultRecipients() []string {
	return c.AllAdmins
}

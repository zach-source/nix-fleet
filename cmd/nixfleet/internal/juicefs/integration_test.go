//go:build integration

// Integration tests for the juicefs package. These exercise real external
// binaries (openssl, op, kubectl, juicefs) and are excluded from the default
// `go test ./...` run.
//
// Run with:  go test -tags=integration ./internal/juicefs/...
//
// Prerequisites:
//   - openssl on PATH (for TestIntegration_Keygen)
//   - op CLI authenticated against a vault you don't mind scratching
//     (for TestIntegration_OpRoundTrip) — set JFS_TEST_VAULT env var
//   - kubectl access to a cluster (for cluster-touching tests)
package juicefs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_Keygen verifies that GenerateEncryptedKeypair produces a
// real PEM that openssl can round-trip with the generated passphrase.
func TestIntegration_Keygen(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not on PATH")
	}

	ctx := context.Background()
	kp, err := GenerateEncryptedKeypair(ctx)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	if len(kp.PrivateKeyPEM) == 0 {
		t.Fatal("empty PrivateKeyPEM")
	}
	if !strings.Contains(string(kp.PrivateKeyPEM), "BEGIN") {
		t.Errorf("PrivateKeyPEM missing PEM header")
	}
	if len(kp.PublicKeyPEM) == 0 {
		t.Fatal("empty PublicKeyPEM")
	}
	if !strings.Contains(string(kp.PublicKeyPEM), "PUBLIC KEY") {
		t.Errorf("PublicKeyPEM missing header: %s", string(kp.PublicKeyPEM))
	}
	if kp.Passphrase == "" {
		t.Fatal("empty Passphrase")
	}
	if len(kp.Fingerprint) != 64 {
		t.Errorf("Fingerprint length = %d, want 64 (sha256 hex)", len(kp.Fingerprint))
	}

	// Round-trip: write PEM to tmp file, ask openssl to decrypt with
	// the returned passphrase. If openssl can derive the pubkey, the
	// passphrase + PEM agree.
	tmpDir := t.TempDir()
	pemPath := filepath.Join(tmpDir, "key.pem")
	if err := os.WriteFile(pemPath, kp.PrivateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "openssl", "rsa",
		"-in", pemPath,
		"-passin", "pass:"+kp.Passphrase,
		"-pubout")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("round-trip openssl rsa -pubout failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "PUBLIC KEY") {
		t.Errorf("round-trip did not produce PUBLIC KEY:\n%s", string(out))
	}
}

// TestIntegration_RandomPassword is a cheap sanity check that doesn't need
// any external tools but lives here because it documents crypto behavior.
func TestIntegration_RandomPassword(t *testing.T) {
	pw1, err := RandomPassword(32)
	if err != nil {
		t.Fatal(err)
	}
	pw2, err := RandomPassword(32)
	if err != nil {
		t.Fatal(err)
	}
	if pw1 == pw2 {
		t.Error("two RandomPassword calls returned the same value")
	}
	// base64-RawURL of 32 bytes is 43 chars (no padding).
	if len(pw1) != 43 {
		t.Errorf("len(pw1) = %d, want 43", len(pw1))
	}
}

// TestIntegration_OpRoundTrip creates, reads, and deletes a scratch 1P item.
// Requires JFS_TEST_VAULT to point at a throwaway vault. Skipped if unset.
func TestIntegration_OpRoundTrip(t *testing.T) {
	vault := os.Getenv("JFS_TEST_VAULT")
	if vault == "" {
		t.Skip("set JFS_TEST_VAULT to a disposable vault to run")
	}
	if _, err := exec.LookPath("op"); err != nil {
		t.Skip("op CLI not on PATH")
	}

	ctx := context.Background()
	title := "juicefs-go-integration-test-DELETEME"

	// Ensure a clean slate.
	_ = exec.Command("op", "item", "delete", title, "--vault", vault).Run()

	exists, err := ItemExists(ctx, vault, title)
	if err != nil {
		t.Fatalf("ItemExists: %v", err)
	}
	if exists {
		t.Fatal("precondition failed: item exists before test")
	}

	err = ItemCreate(ctx, vault, title, []string{
		"marker[text]=hello",
		"secret[password]=value-123",
	})
	if err != nil {
		t.Fatalf("ItemCreate: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("op", "item", "delete", title, "--vault", vault).Run()
	})

	exists, err = ItemExists(ctx, vault, title)
	if err != nil {
		t.Fatalf("ItemExists after create: %v", err)
	}
	if !exists {
		t.Fatal("ItemExists returned false after create")
	}

	got, err := ItemRead(ctx, OpReference(vault, title, "secret"))
	if err != nil {
		t.Fatalf("ItemRead: %v", err)
	}
	if got != "value-123" {
		t.Errorf("ItemRead = %q, want %q", got, "value-123")
	}
}

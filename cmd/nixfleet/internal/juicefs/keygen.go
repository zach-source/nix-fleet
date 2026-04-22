package juicefs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

type Keypair struct {
	PrivateKeyPEM []byte // AES256-encrypted PKCS#1 PEM
	PublicKeyPEM  []byte // PKCS#1 public key PEM
	Passphrase    string // decrypts PrivateKeyPEM
	Fingerprint   string // SHA256 over DER-encoded public key
}

// GenerateEncryptedKeypair produces an AES256-encrypted RSA-4096 keypair by
// shelling out to openssl. Uses temp files under os.TempDir() (typically
// tmpfs on Linux) so the private key never persists.
func GenerateEncryptedKeypair(ctx context.Context) (*Keypair, error) {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	passphrase := base64.StdEncoding.EncodeToString(buf)

	tmpDir, err := os.MkdirTemp("", "juicefs-keygen-")
	if err != nil {
		return nil, fmt.Errorf("mkdir temp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	privPath := filepath.Join(tmpDir, "key.pem")
	pubPath := filepath.Join(tmpDir, "key.pub")

	if out, err := runCombined(ctx, "openssl", "genrsa",
		"-aes256",
		"-passout", "pass:"+passphrase,
		"-out", privPath,
		"4096"); err != nil {
		return nil, fmt.Errorf("openssl genrsa: %w: %s", err, string(out))
	}

	// Decrypt private once to derive public PEM.
	if out, err := runCombined(ctx, "openssl", "rsa",
		"-in", privPath,
		"-passin", "pass:"+passphrase,
		"-pubout",
		"-out", pubPath); err != nil {
		return nil, fmt.Errorf("openssl rsa -pubout: %w: %s", err, string(out))
	}

	// Derive DER from the already-public-PEM (no second RSA decrypt).
	derOut, err := runOutput(ctx, "openssl", "rsa",
		"-in", pubPath,
		"-pubin",
		"-pubout",
		"-outform", "DER")
	if err != nil {
		return nil, fmt.Errorf("openssl rsa -outform DER: %w", err)
	}
	sum := sha256.Sum256(derOut)
	fingerprint := hex.EncodeToString(sum[:])

	privPEM, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	pubPEM, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}

	return &Keypair{
		PrivateKeyPEM: privPEM,
		PublicKeyPEM:  pubPEM,
		Passphrase:    passphrase,
		Fingerprint:   fingerprint,
	}, nil
}

func RandomPassword(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

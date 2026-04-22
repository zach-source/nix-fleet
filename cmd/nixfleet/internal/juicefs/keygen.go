package juicefs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

type Keypair struct {
	PrivateKeyPEM []byte // AES256-encrypted PKCS#8 PEM
	PublicKeyPEM  []byte // PKCS#1 public key PEM
	Passphrase    string // decrypts PrivateKeyPEM
	Fingerprint   string // SHA256 over DER-encoded public key
}

// DefaultRSABits is 2048, not 4096: JuiceFS stores the encrypted PEM in the
// metadata engine's VARCHAR(4096) column; the 4096-bit PEM overflows.
// 2048-bit RSA is still ~112-bit security — well above the useful threshold
// for this threat model (LAN-reachable MinIO on a home NAS).
const DefaultRSABits = 2048

// GenerateEncryptedKeypair produces an AES256-encrypted RSA keypair via
// openssl. Uses a hex passphrase + stdin transport to sidestep two
// openssl/shell edge cases:
//   - base64 passphrases contain +/= which some openssl builds mishandle
//     when passed as `pass:` or `env:` arguments
//   - exported env vars don't always propagate to openssl subprocesses
//     across & && chains on zsh
func GenerateEncryptedKeypair(ctx context.Context) (*Keypair, error) {
	return GenerateEncryptedKeypairBits(ctx, DefaultRSABits)
}

// GenerateEncryptedKeypairBits is the sized variant for callers that
// genuinely need a different key size.
func GenerateEncryptedKeypairBits(ctx context.Context, bits int) (*Keypair, error) {
	// 24 bytes hex-encoded = 48 chars, hex only (openssl/shell safe).
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	passphrase := hex.EncodeToString(raw)

	tmpDir, err := os.MkdirTemp("", "juicefs-keygen-")
	if err != nil {
		return nil, fmt.Errorf("mkdir temp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	privPath := filepath.Join(tmpDir, "key.pem")
	pubPath := filepath.Join(tmpDir, "key.pub")

	if err := runPipe(ctx, []byte(passphrase), "openssl", "genrsa",
		"-aes256",
		"-passout", "stdin",
		"-out", privPath,
		fmt.Sprintf("%d", bits)); err != nil {
		return nil, fmt.Errorf("openssl genrsa: %w", err)
	}

	if err := runPipe(ctx, []byte(passphrase), "openssl", "rsa",
		"-in", privPath,
		"-passin", "stdin",
		"-pubout",
		"-out", pubPath); err != nil {
		return nil, fmt.Errorf("openssl rsa -pubout: %w", err)
	}

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

// RandomPassword returns a hex-encoded cryptographic random string. Use for
// database passwords, MinIO keys, etc. Output length = nBytes * 2 chars.
// Hex-only charset is safe across shell quoting + URL + S3 access-key rules.
func RandomPassword(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// MinIOSecretKey returns a 32-char hex secret suitable for a MinIO service
// account access key (MinIO enforces 8-40 char range on svcacct creation).
func MinIOSecretKey() (string, error) {
	return RandomPassword(16) // 16 bytes → 32 hex chars
}

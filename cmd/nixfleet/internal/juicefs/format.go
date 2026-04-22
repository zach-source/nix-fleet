package juicefs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FormatFilesystem port-forwards PG + MinIO, then runs `juicefs format`.
// Idempotent: bails early if the filesystem is already formatted.
func FormatFilesystem(ctx context.Context, cfg Config, w io.Writer) error {
	fmt.Fprintln(w, "[juicefs] Formatting filesystem (one-time)")

	// Port-forward PG first (needed for the idempotency status check).
	fmt.Fprintln(w, "[juicefs]   port-forwarding juicefs-postgres:5432")
	stopPG, err := cfg.PortForward(ctx, "juicefs-postgres", cfg.PGPort, 5432)
	if err != nil {
		return err
	}
	defer stopPG()

	// Fast-path check needs only PG password + passphrase.
	pgPW, err := ItemRead(ctx, OpReference(cfg.Vault, cfg.PGItem, FieldJuicefsUserPassword))
	if err != nil {
		return fmt.Errorf("read PG password: %w", err)
	}
	passphrase, err := ItemRead(ctx, OpReference(cfg.Vault, cfg.KeyItem, FieldPassphrase))
	if err != nil {
		return fmt.Errorf("read key passphrase: %w", err)
	}
	metaURL := cfg.MetaURLLocal(pgPW)

	statusCmd := exec.CommandContext(ctx, "juicefs", "status", metaURL)
	statusCmd.Env = append(os.Environ(), "JFS_RSA_PASSPHRASE="+passphrase)
	if out, err := statusCmd.CombinedOutput(); err == nil && strings.Contains(string(out), cfg.FSName) {
		fmt.Fprintln(w, "[juicefs]   already formatted — skipping")
		return nil
	}

	// Need MinIO now. Fetch remaining secrets + forward MinIO in parallel.
	fmt.Fprintln(w, "[juicefs]   port-forwarding juicefs-minio:9000")
	stopMinIO, err := cfg.PortForward(ctx, "juicefs-minio", cfg.MinIOPort, 9000)
	if err != nil {
		return err
	}
	defer stopMinIO()

	refs := []string{
		OpReference(cfg.Vault, cfg.MinIOItem, FieldAccessKey),
		OpReference(cfg.Vault, cfg.MinIOItem, FieldSecretKey),
		OpReference(cfg.Vault, cfg.KeyItem, FieldPrivateKey),
	}
	vals, err := parallelItemRead(ctx, refs)
	if err != nil {
		return fmt.Errorf("read MinIO creds + RSA key: %w", err)
	}
	ak, sk, rsaPEM := vals[0], vals[1], vals[2]

	// Stage PEM on disk — juicefs requires --encrypt-rsa-key as a file path.
	tmpDir, err := os.MkdirTemp("", "juicefs-fmt-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	keyPath := filepath.Join(tmpDir, "key.pem")
	if err := os.WriteFile(keyPath, []byte(rsaPEM), 0o400); err != nil {
		return fmt.Errorf("stage PEM: %w", err)
	}

	fmt.Fprintln(w, "[juicefs]   running juicefs format...")
	fmtCmd := exec.CommandContext(ctx, "juicefs", "format",
		"--storage", "s3",
		"--bucket", cfg.BucketURLLocal(),
		"--access-key", ak,
		"--secret-key", sk,
		"--encrypt-rsa-key", keyPath,
		"--block-size", "4096",
		metaURL,
		cfg.FSName,
	)
	fmtCmd.Env = append(os.Environ(), "JFS_RSA_PASSPHRASE="+passphrase)
	fmtCmd.Stdout = w
	fmtCmd.Stderr = w
	if err := fmtCmd.Run(); err != nil {
		return fmt.Errorf("juicefs format: %w", err)
	}
	fmt.Fprintln(w, "[juicefs]   format complete")
	return nil
}

// DumpMetadata runs `juicefs dump` to outPath.
func DumpMetadata(ctx context.Context, cfg Config, outPath string, w io.Writer) error {
	fmt.Fprintf(w, "[juicefs] Dumping metadata to %s\n", outPath)

	stopPG, err := cfg.PortForward(ctx, "juicefs-postgres", cfg.PGPort, 5432)
	if err != nil {
		return err
	}
	defer stopPG()

	refs := []string{
		OpReference(cfg.Vault, cfg.PGItem, FieldJuicefsUserPassword),
		OpReference(cfg.Vault, cfg.KeyItem, FieldPassphrase),
	}
	vals, err := parallelItemRead(ctx, refs)
	if err != nil {
		return fmt.Errorf("read creds: %w", err)
	}
	pgPW, passphrase := vals[0], vals[1]

	cmd := exec.CommandContext(ctx, "juicefs", "dump", cfg.MetaURLLocal(pgPW), outPath)
	cmd.Env = append(os.Environ(), "JFS_RSA_PASSPHRASE="+passphrase)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("juicefs dump: %w", err)
	}
	fmt.Fprintln(w, "[juicefs]   dump complete")
	return nil
}

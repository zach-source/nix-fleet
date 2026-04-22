package juicefs

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/ssh"
)

// PrepareHost SSHes to the configured k0s node and ensures the hostPath
// cache directory exists with the right permissions.
func PrepareHost(ctx context.Context, cfg Config, inv *inventory.Inventory, w io.Writer) error {
	fmt.Fprintf(w, "[juicefs] Preparing host %s (cache dir %s)\n", cfg.K0sNode, cfg.CacheDir)

	host, ok := inv.GetHost(cfg.K0sNode)
	if !ok {
		return fmt.Errorf("host %q not found in inventory", cfg.K0sNode)
	}

	sshCfg := ssh.DefaultConfig()
	if host.SSHUser != "" {
		sshCfg.User = host.SSHUser
	}
	if host.SSHPort != 0 {
		sshCfg.Port = host.SSHPort
	}
	client, err := ssh.NewClient(host.Addr, sshCfg)
	if err != nil {
		return fmt.Errorf("ssh new client %s: %w", cfg.K0sNode, err)
	}
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("ssh connect %s: %w", cfg.K0sNode, err)
	}
	defer client.Close()

	cmd := fmt.Sprintf("mkdir -p %s && chmod 0755 %s", cfg.CacheDir, cfg.CacheDir)
	res, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("mkdir %s on %s: %w", cfg.CacheDir, cfg.K0sNode, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("mkdir %s exited %d: %s", cfg.CacheDir, res.ExitCode, res.Stderr)
	}
	fmt.Fprintln(w, "[juicefs]   ok")
	return nil
}

// ensureItem is the shared idempotent pattern: check for a 1P item, and if
// missing, call buildFields to produce the create-args and do the create.
func ensureItem(ctx context.Context, vault, title string, w io.Writer, buildFields func() ([]string, error)) error {
	fmt.Fprintf(w, "[juicefs] Ensuring 1Password item %q\n", title)
	exists, err := ItemExists(ctx, vault, title)
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintln(w, "[juicefs]   already present — skipping")
		return nil
	}
	fields, err := buildFields()
	if err != nil {
		return err
	}
	if err := ItemCreate(ctx, vault, title, fields); err != nil {
		return err
	}
	fmt.Fprintln(w, "[juicefs]   created")
	return nil
}

func EnsureEncryptionKey(ctx context.Context, cfg Config, w io.Writer) error {
	return ensureItem(ctx, cfg.Vault, cfg.KeyItem, w, func() ([]string, error) {
		fmt.Fprintln(w, "[juicefs]   generating RSA-4096 keypair (openssl)...")
		kp, err := GenerateEncryptedKeypair(ctx)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(w, "[juicefs]   fingerprint %s — STORE AN OFFLINE BACKUP OF THIS KEY\n", kp.Fingerprint)
		return []string{
			FieldPassphrase + "[password]=" + kp.Passphrase,
			FieldPrivateKey + "[password]=" + string(kp.PrivateKeyPEM),
			FieldPublicKey + "[text]=" + string(kp.PublicKeyPEM),
			FieldFingerprint + "[text]=" + kp.Fingerprint,
		}, nil
	})
}

func EnsurePGItem(ctx context.Context, cfg Config, w io.Writer) error {
	return ensureItem(ctx, cfg.Vault, cfg.PGItem, w, func() ([]string, error) {
		pw, err := RandomPassword(32)
		if err != nil {
			return nil, err
		}
		return []string{
			FieldUsername + "[text]=juicefs",
			FieldDatabase + "[text]=juicefs",
			FieldJuicefsUserPassword + "[password]=" + pw,
		}, nil
	})
}

func EnsureMinIOItem(ctx context.Context, cfg Config, w io.Writer) error {
	return ensureItem(ctx, cfg.Vault, cfg.MinIOItem, w, func() ([]string, error) {
		// Root password can be long; MinIO root has no length cap.
		rootPW, err := RandomPassword(32)
		if err != nil {
			return nil, err
		}
		// Service-account secret-key must fit MinIO's 8-40 char range.
		svcSecret, err := MinIOSecretKey()
		if err != nil {
			return nil, err
		}
		return []string{
			FieldRootUser + "[text]=jfsadmin",
			FieldRootPassword + "[password]=" + rootPW,
			FieldAccessKey + "[text]=juicefs-fleet",
			FieldSecretKey + "[password]=" + svcSecret,
		}, nil
	})
}

// EnsureCSISecret composes the juicefs-csi-secret 1P item from the three
// source items. Requires their 1P items to exist (PG/MinIO/key). The PG and
// MinIO *pods* don't need to be running — the composite item just bundles
// credentials with the known cluster-internal service URLs.
func EnsureCSISecret(ctx context.Context, cfg Config, w io.Writer) error {
	return ensureItem(ctx, cfg.Vault, cfg.CSIItem, w, func() ([]string, error) {
		// Fetch the 5 source values in parallel — each op read is a separate
		// CLI invocation with ~100-300ms round-trip.
		refs := []string{
			OpReference(cfg.Vault, cfg.PGItem, FieldJuicefsUserPassword),
			OpReference(cfg.Vault, cfg.MinIOItem, FieldAccessKey),
			OpReference(cfg.Vault, cfg.MinIOItem, FieldSecretKey),
			OpReference(cfg.Vault, cfg.KeyItem, FieldPassphrase),
			OpReference(cfg.Vault, cfg.KeyItem, FieldPrivateKey),
		}
		vals, err := parallelItemRead(ctx, refs)
		if err != nil {
			return nil, fmt.Errorf("read source items: %w", err)
		}
		pgPW, ak, sk, passphrase, rsaKey := vals[0], vals[1], vals[2], vals[3], vals[4]

		envsJSON := fmt.Sprintf(`{"JFS_RSA_PASSPHRASE":%q}`, passphrase)
		return []string{
			FieldName + "[text]=" + cfg.FSName,
			FieldMetaURL + "[password]=" + cfg.MetaURL(pgPW),
			FieldStorage + "[text]=s3",
			FieldBucket + "[text]=" + cfg.BucketURL(),
			FieldAccessKey + "[text]=" + ak,
			FieldSecretKey + "[password]=" + sk,
			FieldEnvs + "[text]=" + envsJSON,
			FieldEncryptRSAKey + "[text]=" + rsaKey,
		}, nil
	})
}

// parallelItemRead runs ItemRead for each ref concurrently. On any error,
// returns that error (other goroutines still complete but their values
// are discarded).
func parallelItemRead(ctx context.Context, refs []string) ([]string, error) {
	vals := make([]string, len(refs))
	errs := make([]error, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref string) {
			defer wg.Done()
			vals[i], errs[i] = ItemRead(ctx, ref)
		}(i, ref)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return vals, nil
}

// WaitForBackingServices polls until PG + MinIO pods are ready. PG and MinIO
// are checked in parallel — both StatefulSets schedule independently.
func WaitForBackingServices(ctx context.Context, cfg Config, w io.Writer) error {
	fmt.Fprintln(w, "[juicefs] Waiting for PG + MinIO pods to be Ready...")
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, selector := range []string{"app=juicefs-postgres", "app=juicefs-minio"} {
		wg.Add(1)
		go func(i int, sel string) {
			defer wg.Done()
			errs[i] = cfg.WaitForPodsReady(waitCtx, sel, 5*time.Second)
		}(i, selector)
	}
	wg.Wait()
	if errs[0] != nil {
		return fmt.Errorf("PG: %w", errs[0])
	}
	if errs[1] != nil {
		return fmt.Errorf("MinIO: %w", errs[1])
	}
	fmt.Fprintln(w, "[juicefs]   both ready")
	return nil
}

// Stdout is a convenience for commands that default to os.Stdout.
func Stdout() io.Writer { return os.Stdout }

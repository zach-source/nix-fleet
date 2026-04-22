// Package juicefs implements bootstrap + format workflows for the JuiceFS
// filesystem used by the fleet. It shells out to the user's local `op` CLI
// (for 1Password), `openssl` (for keygen), `kubectl` (for port-forward +
// secret reads), and `juicefs` (for format + dump). These are the same
// tools the user would run manually; this package just orchestrates them
// idempotently.
package juicefs

import "fmt"

// Config holds bootstrap parameters. Zero values are filled with defaults
// via WithDefaults().
type Config struct {
	// Vault is the 1Password vault where all juicefs items live.
	Vault string
	// Namespace is the k8s namespace housing PG + MinIO + the CSI secret.
	Namespace string
	// FSName is the JuiceFS filesystem name (used in juicefs format).
	FSName string
	// Kubeconfig is the path to the kubeconfig used for kubectl calls.
	// If empty, kubectl uses its default (KUBECONFIG env or ~/.kube/config).
	Kubeconfig string
	// KubeContext is an optional context name passed to kubectl.
	KubeContext string
	// CacheDir is the hostPath on the k0s node used by CSI mount pods.
	CacheDir string
	// K0sNode is the inventory host name to SSH into for host-level setup
	// (creating the cache dir).
	K0sNode string

	// Item titles in the vault.
	KeyItem   string
	PGItem    string
	MinIOItem string
	CSIItem   string

	// Network-accessible endpoints for format-from-outside.
	// These default to localhost when port-forward is used.
	PGHost    string
	PGPort    int
	MinIOHost string
	MinIOPort int
}

// WithDefaults returns c with zero fields filled with fleet-standard
// defaults. Call once in a command's RunE.
func (c Config) WithDefaults() Config {
	if c.Vault == "" {
		c.Vault = "Personal Agents"
	}
	if c.Namespace == "" {
		c.Namespace = "juicefs-system"
	}
	if c.FSName == "" {
		c.FSName = "fleet"
	}
	if c.CacheDir == "" {
		c.CacheDir = "/var/lib/juicefs/cache"
	}
	if c.K0sNode == "" {
		c.K0sNode = "gti"
	}
	if c.KeyItem == "" {
		c.KeyItem = "juicefs-encryption-key"
	}
	if c.PGItem == "" {
		c.PGItem = "juicefs-postgres"
	}
	if c.MinIOItem == "" {
		c.MinIOItem = "juicefs-minio"
	}
	if c.CSIItem == "" {
		c.CSIItem = "juicefs-csi-secret"
	}
	if c.PGHost == "" {
		c.PGHost = "localhost"
	}
	if c.PGPort == 0 {
		c.PGPort = 5432
	}
	if c.MinIOHost == "" {
		c.MinIOHost = "localhost"
	}
	if c.MinIOPort == 0 {
		c.MinIOPort = 9000
	}
	return c
}

// MetaURL returns the cluster-internal postgres:// metadata URL for the
// JuiceFS filesystem. password is the juicefs user's PG password.
func (c Config) MetaURL(password string) string {
	return fmt.Sprintf(
		"postgres://juicefs:%s@juicefs-postgres.%s.svc.cluster.local:5432/juicefs?sslmode=disable",
		password, c.Namespace)
}

// MetaURLLocal returns the metadata URL pointing at a local port-forwarded
// PG (used by format/dump on an operator machine).
func (c Config) MetaURLLocal(password string) string {
	return fmt.Sprintf(
		"postgres://juicefs:%s@%s:%d/juicefs?sslmode=disable",
		password, c.PGHost, c.PGPort)
}

// BucketURL returns the cluster-internal MinIO S3 bucket URL.
func (c Config) BucketURL() string {
	return fmt.Sprintf(
		"http://juicefs-minio.%s.svc.cluster.local:9000/%s",
		c.Namespace, c.FSName)
}

// BucketURLLocal returns the bucket URL pointing at local port-forwarded MinIO.
func (c Config) BucketURLLocal() string {
	return fmt.Sprintf("http://%s:%d/%s", c.MinIOHost, c.MinIOPort, c.FSName)
}

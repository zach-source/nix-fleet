package pki

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/ssh"
)

// DeployConfig configures PKI deployment
type DeployConfig struct {
	PKIDir      string   // Directory containing PKI files (default: secrets/pki)
	DestDir     string   // Destination directory on hosts (default: /etc/nixfleet/pki)
	Identities  []string // Age identity files for decryption
	TrustSystem bool     // Add CA to system trust store
	CAOnly      bool     // Only deploy CA certificate
}

// DefaultDeployConfig returns default PKI deployment config
func DefaultDeployConfig() *DeployConfig {
	return &DeployConfig{
		PKIDir:      "secrets/pki",
		DestDir:     "/etc/nixfleet/pki",
		TrustSystem: false,
		CAOnly:      false,
	}
}

// Deployer handles PKI deployment to hosts
type Deployer struct {
	store  *Store
	config *DeployConfig
}

// NewDeployer creates a new PKI deployer
func NewDeployer(config *DeployConfig) *Deployer {
	store := NewStore(config.PKIDir, nil, config.Identities)
	return &Deployer{
		store:  store,
		config: config,
	}
}

// DeployResult contains the result of deploying PKI to a host
type DeployResult struct {
	Host         string
	Success      bool
	CADeployed   bool
	CertDeployed bool
	KeyDeployed  bool
	TrustUpdated bool
	CertRenewed  bool
	Error        string
	CertInfo     *CertInfo
}

// IsEnabled checks if PKI is configured and ready for deployment
func (d *Deployer) IsEnabled() bool {
	return d.store.CAExists()
}

// Deploy deploys PKI to a single host
func (d *Deployer) Deploy(ctx context.Context, client *ssh.Client, host *inventory.Host) *DeployResult {
	result := &DeployResult{
		Host: host.Name,
	}

	// Read CA certificate
	caCertPEM, err := os.ReadFile(d.store.GetCACertPath())
	if err != nil {
		result.Error = fmt.Sprintf("reading CA certificate: %v", err)
		return result
	}

	// Create PKI directory
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s && sudo chmod 755 %s", d.config.DestDir, d.config.DestDir)
	if _, err := client.Exec(ctx, mkdirCmd); err != nil {
		result.Error = fmt.Sprintf("creating directory: %v", err)
		return result
	}

	// Deploy CA certificate
	caCertDest := d.config.DestDir + "/ca.crt"
	if err := d.deployFileContent(ctx, client, caCertPEM, caCertDest, "0644"); err != nil {
		result.Error = fmt.Sprintf("deploying CA cert: %v", err)
		return result
	}
	result.CADeployed = true

	// Update system trust store if requested
	if d.config.TrustSystem {
		if err := d.updateSystemTrust(ctx, client, host.Base, caCertDest); err != nil {
			// Non-fatal, just log
			result.Error = fmt.Sprintf("warning: trust update failed: %v", err)
		} else {
			result.TrustUpdated = true
		}
	}

	// Deploy host certificate and key (unless CA-only mode)
	if !d.config.CAOnly && d.store.HostCertExists(host.Name) {
		hostCert, err := d.store.LoadHostCert(ctx, host.Name)
		if err != nil {
			result.Error = fmt.Sprintf("loading host cert: %v", err)
			return result
		}

		result.CertInfo = &CertInfo{
			Hostname:  hostCert.Hostname,
			Serial:    hostCert.Serial,
			NotBefore: hostCert.NotBefore,
			NotAfter:  hostCert.NotAfter,
			SANs:      hostCert.SANs,
		}

		// Calculate days left and status
		now := time.Now()
		result.CertInfo.DaysLeft = int(hostCert.NotAfter.Sub(now).Hours() / 24)
		switch {
		case now.After(hostCert.NotAfter):
			result.CertInfo.Status = "expired"
		case result.CertInfo.DaysLeft <= 30:
			result.CertInfo.Status = "expiring"
		default:
			result.CertInfo.Status = "valid"
		}

		// Deploy host certificate
		hostCertDest := d.config.DestDir + "/host.crt"
		if err := d.deployFileContent(ctx, client, hostCert.CertPEM, hostCertDest, "0644"); err != nil {
			result.Error = fmt.Sprintf("deploying host cert: %v", err)
			return result
		}
		result.CertDeployed = true

		// Deploy host key (restricted permissions)
		hostKeyDest := d.config.DestDir + "/host.key"
		if err := d.deployFileContent(ctx, client, hostCert.KeyPEM, hostKeyDest, "0600"); err != nil {
			result.Error = fmt.Sprintf("deploying host key: %v", err)
			return result
		}
		result.KeyDeployed = true
	}

	result.Success = true
	return result
}

// CheckRenewalNeeded checks if any host certificates need renewal
func (d *Deployer) CheckRenewalNeeded(ctx context.Context, daysThreshold int) ([]*RenewalInfo, error) {
	hosts, err := d.store.ListHostCerts()
	if err != nil {
		return nil, err
	}

	var needsRenewal []*RenewalInfo
	for _, hostname := range hosts {
		info, err := d.store.GetCertInfo(hostname)
		if err != nil {
			continue
		}

		if info.DaysLeft <= daysThreshold {
			needsRenewal = append(needsRenewal, &RenewalInfo{
				Hostname: hostname,
				CertInfo: info,
				Reason:   fmt.Sprintf("expires in %d days", info.DaysLeft),
			})
		}
	}

	return needsRenewal, nil
}

// RenewCert renews a host certificate
func (d *Deployer) RenewCert(ctx context.Context, hostname string, sans []string, validity time.Duration) (*IssuedCert, error) {
	// Load CA
	ca, err := d.store.LoadCA(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	// Get existing cert info to preserve SANs if not specified
	if len(sans) == 0 {
		existingInfo, err := d.store.GetCertInfo(hostname)
		if err == nil {
			sans = existingInfo.SANs
		}
	}

	// Issue new certificate
	req := &CertRequest{
		Hostname: hostname,
		SANs:     sans,
		Validity: validity,
	}

	cert, err := ca.IssueCert(req)
	if err != nil {
		return nil, fmt.Errorf("issuing certificate: %w", err)
	}

	// Save new certificate
	if err := d.store.SaveHostCert(cert); err != nil {
		return nil, fmt.Errorf("saving certificate: %w", err)
	}

	return cert, nil
}

// RenewalInfo contains information about a certificate needing renewal
type RenewalInfo struct {
	Hostname string
	CertInfo *CertInfo
	Reason   string
}

// RevokeCert marks a certificate as revoked (adds to CRL)
func (d *Deployer) RevokeCert(ctx context.Context, hostname string) error {
	// For now, we implement revocation by removing the certificate
	// A full implementation would maintain a CRL
	certPath := d.store.GetHostCertPath(hostname)
	keyPath := d.store.GetHostKeyPath(hostname)

	if err := os.Remove(certPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing certificate: %w", err)
	}
	if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing key: %w", err)
	}

	// TODO: Add to CRL file for proper revocation checking
	return nil
}

// helper functions

func (d *Deployer) deployFileContent(ctx context.Context, client *ssh.Client, content []byte, destPath, mode string) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	cmd := fmt.Sprintf("echo '%s' | base64 -d | sudo tee %s > /dev/null && sudo chmod %s %s",
		encoded, destPath, mode, destPath)
	_, err := client.Exec(ctx, cmd)
	return err
}

func (d *Deployer) updateSystemTrust(ctx context.Context, client *ssh.Client, base, caCertPath string) error {
	var updateCmd string
	switch base {
	case "ubuntu":
		updateCmd = fmt.Sprintf("sudo cp %s /usr/local/share/ca-certificates/nixfleet-ca.crt && sudo update-ca-certificates", caCertPath)
	case "nixos", "darwin":
		// NixOS/darwin handle this via configuration, not runtime commands
		return nil
	default:
		return fmt.Errorf("unsupported base: %s", base)
	}

	_, err := client.Exec(ctx, updateCmd)
	return err
}

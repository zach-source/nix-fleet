package pki

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Store manages PKI files in the secrets directory
type Store struct {
	baseDir    string   // Base directory (usually "secrets/pki")
	recipients []string // Age recipients for encryption
	identities []string // Age identity files for decryption
}

// NewStore creates a new PKI store
func NewStore(baseDir string, recipients, identities []string) *Store {
	return &Store{
		baseDir:    baseDir,
		recipients: recipients,
		identities: identities,
	}
}

// CAExists checks if a CA has been initialized
func (s *Store) CAExists() bool {
	caCertPath := filepath.Join(s.baseDir, "ca", "root.crt")
	caKeyPath := filepath.Join(s.baseDir, "ca", "root.key.age")

	_, certErr := os.Stat(caCertPath)
	_, keyErr := os.Stat(caKeyPath)

	return certErr == nil && keyErr == nil
}

// SaveCA saves the CA certificate and encrypted private key
func (s *Store) SaveCA(ca *CA) error {
	caDir := filepath.Join(s.baseDir, "ca")
	if err := os.MkdirAll(caDir, 0755); err != nil {
		return fmt.Errorf("creating CA directory: %w", err)
	}

	// Save certificate (public, not encrypted)
	certPath := filepath.Join(caDir, "root.crt")
	if err := os.WriteFile(certPath, ca.CertPEM, 0644); err != nil {
		return fmt.Errorf("writing CA certificate: %w", err)
	}

	// Encrypt and save private key
	keyPath := filepath.Join(caDir, "root.key.age")
	if err := s.encryptAndSave(ca.KeyPEM, keyPath); err != nil {
		return fmt.Errorf("encrypting CA private key: %w", err)
	}

	return nil
}

// LoadCA loads the CA from disk
func (s *Store) LoadCA(ctx context.Context) (*CA, error) {
	caDir := filepath.Join(s.baseDir, "ca")

	// Read certificate
	certPath := filepath.Join(caDir, "root.crt")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA certificate: %w", err)
	}

	// Decrypt and read private key
	keyPath := filepath.Join(caDir, "root.key.age")
	keyPEM, err := s.decryptFile(ctx, keyPath)
	if err != nil {
		return nil, fmt.Errorf("decrypting CA private key: %w", err)
	}

	return LoadCA(certPEM, keyPEM)
}

// IntermediateCAExists checks if an intermediate CA has been initialized
func (s *Store) IntermediateCAExists() bool {
	intermediateCertPath := filepath.Join(s.baseDir, "ca", "intermediate.crt")
	intermediateKeyPath := filepath.Join(s.baseDir, "ca", "intermediate.key.age")

	_, certErr := os.Stat(intermediateCertPath)
	_, keyErr := os.Stat(intermediateKeyPath)

	return certErr == nil && keyErr == nil
}

// SaveIntermediateCA saves the intermediate CA certificate and encrypted private key
func (s *Store) SaveIntermediateCA(ica *IntermediateCA) error {
	caDir := filepath.Join(s.baseDir, "ca")
	if err := os.MkdirAll(caDir, 0755); err != nil {
		return fmt.Errorf("creating CA directory: %w", err)
	}

	// Save intermediate certificate (public, not encrypted)
	certPath := filepath.Join(caDir, "intermediate.crt")
	if err := os.WriteFile(certPath, ica.CertPEM, 0644); err != nil {
		return fmt.Errorf("writing intermediate CA certificate: %w", err)
	}

	// Save the full chain (intermediate + root)
	chainPath := filepath.Join(caDir, "chain.crt")
	if err := os.WriteFile(chainPath, ica.ChainPEM, 0644); err != nil {
		return fmt.Errorf("writing CA chain: %w", err)
	}

	// Encrypt and save private key
	keyPath := filepath.Join(caDir, "intermediate.key.age")
	if err := s.encryptAndSave(ica.KeyPEM, keyPath); err != nil {
		return fmt.Errorf("encrypting intermediate CA private key: %w", err)
	}

	return nil
}

// LoadIntermediateCA loads the intermediate CA from disk
func (s *Store) LoadIntermediateCA(ctx context.Context) (*IntermediateCA, error) {
	caDir := filepath.Join(s.baseDir, "ca")

	// Read intermediate certificate
	certPath := filepath.Join(caDir, "intermediate.crt")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading intermediate CA certificate: %w", err)
	}

	// Read root certificate
	rootCertPath := filepath.Join(caDir, "root.crt")
	rootCertPEM, err := os.ReadFile(rootCertPath)
	if err != nil {
		return nil, fmt.Errorf("reading root CA certificate: %w", err)
	}

	// Decrypt and read private key
	keyPath := filepath.Join(caDir, "intermediate.key.age")
	keyPEM, err := s.decryptFile(ctx, keyPath)
	if err != nil {
		return nil, fmt.Errorf("decrypting intermediate CA private key: %w", err)
	}

	return LoadIntermediateCA(certPEM, keyPEM, rootCertPEM)
}

// GetIntermediateCertPath returns the path to the intermediate CA certificate
func (s *Store) GetIntermediateCertPath() string {
	return filepath.Join(s.baseDir, "ca", "intermediate.crt")
}

// GetChainCertPath returns the path to the full certificate chain
func (s *Store) GetChainCertPath() string {
	return filepath.Join(s.baseDir, "ca", "chain.crt")
}

// SaveHostCert saves a host certificate and encrypted private key
// Supports named certificates: secrets/pki/hosts/{hostname}/{name}.crt
// If the certificate includes a chain (from intermediate CA), saves it as {name}.chain.crt
func (s *Store) SaveHostCert(cert *IssuedCert) error {
	certName := cert.Name
	if certName == "" {
		certName = "host"
	}

	hostDir := filepath.Join(s.baseDir, "hosts", cert.Hostname)
	if err := os.MkdirAll(hostDir, 0755); err != nil {
		return fmt.Errorf("creating host directory: %w", err)
	}

	// Save certificate (public)
	certPath := filepath.Join(hostDir, certName+".crt")
	if err := os.WriteFile(certPath, cert.CertPEM, 0644); err != nil {
		return fmt.Errorf("writing host certificate: %w", err)
	}

	// Save chain if present (cert + intermediate + root)
	if len(cert.ChainPEM) > 0 {
		chainPath := filepath.Join(hostDir, certName+".chain.crt")
		if err := os.WriteFile(chainPath, cert.ChainPEM, 0644); err != nil {
			return fmt.Errorf("writing certificate chain: %w", err)
		}
	}

	// Encrypt and save private key
	keyPath := filepath.Join(hostDir, certName+".key.age")
	if err := s.encryptAndSave(cert.KeyPEM, keyPath); err != nil {
		return fmt.Errorf("encrypting host private key: %w", err)
	}

	return nil
}

// LoadHostCert loads a host certificate from disk (default "host" name)
func (s *Store) LoadHostCert(ctx context.Context, hostname string) (*IssuedCert, error) {
	return s.LoadNamedCert(ctx, hostname, "host")
}

// LoadNamedCert loads a named certificate for a host
func (s *Store) LoadNamedCert(ctx context.Context, hostname, certName string) (*IssuedCert, error) {
	if certName == "" {
		certName = "host"
	}

	hostDir := filepath.Join(s.baseDir, "hosts", hostname)

	// Read certificate
	certPath := filepath.Join(hostDir, certName+".crt")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("certificate %s/%s not found", hostname, certName)
		}
		return nil, fmt.Errorf("reading host certificate: %w", err)
	}

	// Read chain if it exists (optional)
	var chainPEM []byte
	chainPath := filepath.Join(hostDir, certName+".chain.crt")
	if chainData, err := os.ReadFile(chainPath); err == nil {
		chainPEM = chainData
	}

	// Decrypt private key
	keyPath := filepath.Join(hostDir, certName+".key.age")
	keyPEM, err := s.decryptFile(ctx, keyPath)
	if err != nil {
		return nil, fmt.Errorf("decrypting host private key: %w", err)
	}

	// Parse certificate to extract metadata
	info, err := ParseCertInfo(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate info: %w", err)
	}

	return &IssuedCert{
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		ChainPEM:   chainPEM,
		Hostname:   hostname,
		Name:       certName,
		Serial:     info.Serial,
		NotBefore:  info.NotBefore,
		NotAfter:   info.NotAfter,
		SANs:       info.SANs,
		Thumbprint: info.Thumbprint,
	}, nil
}

// HostCertExists checks if a host certificate exists (default "host" name)
func (s *Store) HostCertExists(hostname string) bool {
	return s.NamedCertExists(hostname, "host")
}

// NamedCertExists checks if a named certificate exists for a host
func (s *Store) NamedCertExists(hostname, certName string) bool {
	if certName == "" {
		certName = "host"
	}
	certPath := filepath.Join(s.baseDir, "hosts", hostname, certName+".crt")
	_, err := os.Stat(certPath)
	return err == nil
}

// ListHostCerts returns a list of all hostnames with certificates
func (s *Store) ListHostCerts() ([]string, error) {
	hostDir := filepath.Join(s.baseDir, "hosts")

	entries, err := os.ReadDir(hostDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var hosts []string
	for _, entry := range entries {
		if entry.IsDir() {
			hosts = append(hosts, entry.Name())
		}
	}

	return hosts, nil
}

// ListHostNamedCerts returns all certificate names for a host
func (s *Store) ListHostNamedCerts(hostname string) ([]string, error) {
	hostDir := filepath.Join(s.baseDir, "hosts", hostname)

	entries, err := os.ReadDir(hostDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var certs []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".crt") {
			certName := strings.TrimSuffix(entry.Name(), ".crt")
			certs = append(certs, certName)
		}
	}

	return certs, nil
}

// GetCACertPath returns the path to the CA certificate
func (s *Store) GetCACertPath() string {
	return filepath.Join(s.baseDir, "ca", "root.crt")
}

// GetHostCertPath returns the path to a host's default certificate
func (s *Store) GetHostCertPath(hostname string) string {
	return s.GetNamedCertPath(hostname, "host")
}

// GetNamedCertPath returns the path to a named certificate
func (s *Store) GetNamedCertPath(hostname, certName string) string {
	if certName == "" {
		certName = "host"
	}
	return filepath.Join(s.baseDir, "hosts", hostname, certName+".crt")
}

// GetHostKeyPath returns the path to a host's default encrypted private key
func (s *Store) GetHostKeyPath(hostname string) string {
	return s.GetNamedKeyPath(hostname, "host")
}

// GetNamedKeyPath returns the path to a named certificate's encrypted key
func (s *Store) GetNamedKeyPath(hostname, certName string) string {
	if certName == "" {
		certName = "host"
	}
	return filepath.Join(s.baseDir, "hosts", hostname, certName+".key.age")
}

// CertInfo contains parsed certificate information
type CertInfo struct {
	Hostname   string
	Name       string // Certificate name (e.g., "host", "web", "api")
	Serial     string
	NotBefore  time.Time
	NotAfter   time.Time
	SANs       []string
	Thumbprint string
	DaysLeft   int
	Status     string // "valid", "expiring", "expired"
}

// ParseCertInfo parses a PEM-encoded certificate and returns its metadata
func ParseCertInfo(certPEM []byte) (*CertInfo, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}

	// Collect SANs
	var sans []string
	sans = append(sans, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		sans = append(sans, ip.String())
	}

	// Calculate days left and status
	now := time.Now()
	daysLeft := int(cert.NotAfter.Sub(now).Hours() / 24)

	var status string
	switch {
	case now.After(cert.NotAfter):
		status = "expired"
	case daysLeft <= 30:
		status = "expiring"
	default:
		status = "valid"
	}

	return &CertInfo{
		Hostname:   cert.Subject.CommonName,
		Serial:     cert.SerialNumber.String(),
		NotBefore:  cert.NotBefore,
		NotAfter:   cert.NotAfter,
		SANs:       sans,
		Thumbprint: computeThumbprint(block.Bytes),
		DaysLeft:   daysLeft,
		Status:     status,
	}, nil
}

// GetCertInfo reads and parses a host's default certificate from disk
func (s *Store) GetCertInfo(hostname string) (*CertInfo, error) {
	return s.GetNamedCertInfo(hostname, "host")
}

// GetNamedCertInfo reads and parses a named certificate from disk
func (s *Store) GetNamedCertInfo(hostname, certName string) (*CertInfo, error) {
	if certName == "" {
		certName = "host"
	}
	certPath := filepath.Join(s.baseDir, "hosts", hostname, certName+".crt")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	info, err := ParseCertInfo(certPEM)
	if err != nil {
		return nil, err
	}
	info.Name = certName
	return info, nil
}

// encryption helpers

func (s *Store) encryptAndSave(data []byte, path string) error {
	if len(s.recipients) == 0 {
		return fmt.Errorf("no age recipients configured")
	}

	args := []string{"--encrypt", "--armor"}
	for _, r := range s.recipients {
		args = append(args, "-r", r)
	}
	args = append(args, "-o", path)

	cmd := exec.Command("age", args...)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("age encrypt failed: %s", stderr.String())
	}

	return nil
}

func (s *Store) decryptFile(ctx context.Context, path string) ([]byte, error) {
	args := []string{"--decrypt"}
	for _, id := range s.identities {
		args = append(args, "-i", id)
	}
	args = append(args, path)

	cmd := exec.CommandContext(ctx, "age", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("age decrypt failed: %s", stderr.String())
	}

	return stdout.Bytes(), nil
}

// Package pki implements certificate authority and TLS certificate management for NixFleet
package pki

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// CA represents a Certificate Authority for the fleet
type CA struct {
	Certificate *x509.Certificate
	PrivateKey  ed25519.PrivateKey
	CertPEM     []byte
	KeyPEM      []byte
}

// IntermediateCA represents an intermediate Certificate Authority
// It includes the chain back to the root CA for certificate validation
type IntermediateCA struct {
	Certificate *x509.Certificate
	PrivateKey  ed25519.PrivateKey
	CertPEM     []byte
	KeyPEM      []byte
	ChainPEM    []byte // Full chain: intermediate + root
	RootCertPEM []byte // Root CA certificate only
}

// IntermediateCAConfig holds configuration for intermediate CA initialization
type IntermediateCAConfig struct {
	CommonName   string
	Organization string
	Validity     time.Duration
}

// DefaultIntermediateCAConfig returns sensible defaults for an intermediate CA
func DefaultIntermediateCAConfig() *IntermediateCAConfig {
	return &IntermediateCAConfig{
		CommonName:   "NixFleet Intermediate CA",
		Organization: "NixFleet",
		Validity:     5 * 365 * 24 * time.Hour, // 5 years
	}
}

// CAConfig holds configuration for CA initialization
type CAConfig struct {
	CommonName   string
	Organization string
	Validity     time.Duration
}

// DefaultCAConfig returns sensible defaults for a fleet CA
func DefaultCAConfig() *CAConfig {
	return &CAConfig{
		CommonName:   "NixFleet Root CA",
		Organization: "NixFleet",
		Validity:     10 * 365 * 24 * time.Hour, // 10 years
	}
}

// InitCA creates a new Certificate Authority
func InitCA(cfg *CAConfig) (*CA, error) {
	// Generate Ed25519 key pair
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key pair: %w", err)
	}

	// Generate serial number
	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   cfg.CommonName,
			Organization: []string{cfg.Organization},
		},
		NotBefore:             now,
		NotAfter:              now.Add(cfg.Validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
		MaxPathLenZero:        false,
	}

	// Self-sign the CA certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, pubKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyPEM, err := marshalEd25519PrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("encoding CA private key: %w", err)
	}

	return &CA{
		Certificate: cert,
		PrivateKey:  privKey,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
	}, nil
}

// LoadCA loads a CA from PEM-encoded certificate and key
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	// Parse certificate
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode CA certificate PEM")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	// Parse private key
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode CA private key PEM")
	}

	privKey, err := parseEd25519PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA private key: %w", err)
	}

	return &CA{
		Certificate: cert,
		PrivateKey:  privKey,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
	}, nil
}

// InitIntermediateCA creates an intermediate CA signed by the root CA
func (ca *CA) InitIntermediateCA(cfg *IntermediateCAConfig) (*IntermediateCA, error) {
	if cfg == nil {
		cfg = DefaultIntermediateCAConfig()
	}

	// Generate Ed25519 key pair for the intermediate
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating intermediate CA key pair: %w", err)
	}

	// Generate serial number
	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	now := time.Now()

	// Ensure intermediate doesn't outlive root
	validity := cfg.Validity
	if now.Add(validity).After(ca.Certificate.NotAfter) {
		validity = ca.Certificate.NotAfter.Sub(now) - 24*time.Hour // 1 day buffer
		if validity <= 0 {
			return nil, fmt.Errorf("root CA expires too soon to create intermediate")
		}
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   cfg.CommonName,
			Organization: []string{cfg.Organization},
		},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,    // Can only sign end-entity certs
		MaxPathLenZero:        true, // MaxPathLen=0 is intentional
	}

	// Sign with root CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Certificate, pubKey, ca.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("creating intermediate CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing intermediate CA certificate: %w", err)
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyPEM, err := marshalEd25519PrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("encoding intermediate CA private key: %w", err)
	}

	// Build chain: intermediate + root
	chainPEM := append(certPEM, ca.CertPEM...)

	return &IntermediateCA{
		Certificate: cert,
		PrivateKey:  privKey,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		ChainPEM:    chainPEM,
		RootCertPEM: ca.CertPEM,
	}, nil
}

// LoadIntermediateCA loads an intermediate CA from PEM-encoded certificate, key, and root cert
func LoadIntermediateCA(certPEM, keyPEM, rootCertPEM []byte) (*IntermediateCA, error) {
	// Parse certificate
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode intermediate CA certificate PEM")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing intermediate CA certificate: %w", err)
	}

	// Verify it's a CA
	if !cert.IsCA {
		return nil, fmt.Errorf("certificate is not a CA")
	}

	// Parse private key
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode intermediate CA private key PEM")
	}

	privKey, err := parseEd25519PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing intermediate CA private key: %w", err)
	}

	// Build chain: intermediate + root
	chainPEM := append(certPEM, rootCertPEM...)

	return &IntermediateCA{
		Certificate: cert,
		PrivateKey:  privKey,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		ChainPEM:    chainPEM,
		RootCertPEM: rootCertPEM,
	}, nil
}

// CertRequest holds parameters for issuing a host certificate
type CertRequest struct {
	Hostname string
	Name     string        // Certificate name (e.g., "web", "api"). Empty = default "host"
	SANs     []string      // Additional DNS names and IP addresses
	Validity time.Duration // Certificate validity period
}

// IssuedCert represents an issued certificate with its key material
type IssuedCert struct {
	CertPEM    []byte
	KeyPEM     []byte
	ChainPEM   []byte // Full chain: cert + intermediate(s) + root (optional)
	Hostname   string
	Name       string // Certificate name within the host
	Serial     string
	NotBefore  time.Time
	NotAfter   time.Time
	SANs       []string
	Thumbprint string
}

// CertInstallSpec defines how/where to install a certificate on a host
type CertInstallSpec struct {
	Name        string `json:"name"`                  // Certificate name (matches IssuedCert.Name)
	InstallPath string `json:"installPath,omitempty"` // Directory to install certs (default: /etc/nixfleet/pki)
	CertFile    string `json:"certFile,omitempty"`    // Certificate filename (default: {name}.crt)
	KeyFile     string `json:"keyFile,omitempty"`     // Key filename (default: {name}.key)
	Owner       string `json:"owner,omitempty"`       // File owner (default: root)
	Group       string `json:"group,omitempty"`       // File group (default: root)
	CertMode    string `json:"certMode,omitempty"`    // Cert permissions (default: 0644)
	KeyMode     string `json:"keyMode,omitempty"`     // Key permissions (default: 0600)
}

// DefaultCertInstallSpec returns default install spec for a certificate
func DefaultCertInstallSpec(name string) *CertInstallSpec {
	if name == "" {
		name = "host"
	}
	return &CertInstallSpec{
		Name:        name,
		InstallPath: "/etc/nixfleet/pki",
		CertFile:    name + ".crt",
		KeyFile:     name + ".key",
		Owner:       "root",
		Group:       "root",
		CertMode:    "0644",
		KeyMode:     "0600",
	}
}

// FullCertPath returns the full path to the certificate file
func (s *CertInstallSpec) FullCertPath() string {
	return s.InstallPath + "/" + s.CertFile
}

// FullKeyPath returns the full path to the key file
func (s *CertInstallSpec) FullKeyPath() string {
	return s.InstallPath + "/" + s.KeyFile
}

// IssueCert issues a new certificate for a host
func (ca *CA) IssueCert(req *CertRequest) (*IssuedCert, error) {
	// Generate Ed25519 key pair for the host
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating host key pair: %w", err)
	}

	// Generate serial number
	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	now := time.Now()
	validity := req.Validity
	if validity == 0 {
		validity = 365 * 24 * time.Hour // Default 1 year
	}

	// Parse SANs into DNS names and IP addresses
	var dnsNames []string
	var ipAddresses []net.IP

	// Always include the hostname
	dnsNames = append(dnsNames, req.Hostname)

	for _, san := range req.SANs {
		if ip := net.ParseIP(san); ip != nil {
			ipAddresses = append(ipAddresses, ip)
		} else {
			dnsNames = append(dnsNames, san)
		}
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   req.Hostname,
			Organization: ca.Certificate.Subject.Organization,
		},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}

	// Sign with CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Certificate, pubKey, ca.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("signing host certificate: %w", err)
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyPEM, err := marshalEd25519PrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("encoding host private key: %w", err)
	}

	// Compute thumbprint
	thumbprint := computeThumbprint(certDER)

	// Determine certificate name
	certName := req.Name
	if certName == "" {
		certName = "host"
	}

	return &IssuedCert{
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		Hostname:   req.Hostname,
		Name:       certName,
		Serial:     serialNumber.String(),
		NotBefore:  now,
		NotAfter:   now.Add(validity),
		SANs:       append(dnsNames, ipStrings(ipAddresses)...),
		Thumbprint: thumbprint,
	}, nil
}

// Verify checks if a certificate is valid and signed by this CA
func (ca *CA) Verify(certPEM []byte) error {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return fmt.Errorf("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parsing certificate: %w", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca.Certificate)

	opts := x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}

	if _, err := cert.Verify(opts); err != nil {
		return fmt.Errorf("certificate verification failed: %w", err)
	}

	return nil
}

// IssueCert issues a new certificate for a host, signed by the intermediate CA
// The returned certificate includes the full chain (cert + intermediate + root)
func (ica *IntermediateCA) IssueCert(req *CertRequest) (*IssuedCert, error) {
	// Generate Ed25519 key pair for the host
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating host key pair: %w", err)
	}

	// Generate serial number
	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	now := time.Now()
	validity := req.Validity
	if validity == 0 {
		validity = 365 * 24 * time.Hour // Default 1 year
	}

	// Ensure cert doesn't outlive intermediate
	if now.Add(validity).After(ica.Certificate.NotAfter) {
		validity = ica.Certificate.NotAfter.Sub(now) - 24*time.Hour
		if validity <= 0 {
			return nil, fmt.Errorf("intermediate CA expires too soon to issue certificate")
		}
	}

	// Parse SANs into DNS names and IP addresses
	var dnsNames []string
	var ipAddresses []net.IP

	// Always include the hostname
	dnsNames = append(dnsNames, req.Hostname)

	for _, san := range req.SANs {
		if ip := net.ParseIP(san); ip != nil {
			ipAddresses = append(ipAddresses, ip)
		} else {
			dnsNames = append(dnsNames, san)
		}
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   req.Hostname,
			Organization: ica.Certificate.Subject.Organization,
		},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}

	// Sign with intermediate CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, ica.Certificate, pubKey, ica.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("signing host certificate: %w", err)
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyPEM, err := marshalEd25519PrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("encoding host private key: %w", err)
	}

	// Build full chain: cert + intermediate + root
	chainPEM := append(certPEM, ica.ChainPEM...)

	// Compute thumbprint
	thumbprint := computeThumbprint(certDER)

	// Determine certificate name
	certName := req.Name
	if certName == "" {
		certName = "host"
	}

	return &IssuedCert{
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		ChainPEM:   chainPEM,
		Hostname:   req.Hostname,
		Name:       certName,
		Serial:     serialNumber.String(),
		NotBefore:  now,
		NotAfter:   now.Add(validity),
		SANs:       append(dnsNames, ipStrings(ipAddresses)...),
		Thumbprint: thumbprint,
	}, nil
}

// Verify checks if a certificate is valid and signed by this intermediate CA chain
func (ica *IntermediateCA) Verify(certPEM []byte) error {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return fmt.Errorf("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parsing certificate: %w", err)
	}

	// Parse root certificate
	rootBlock, _ := pem.Decode(ica.RootCertPEM)
	if rootBlock == nil {
		return fmt.Errorf("failed to decode root certificate PEM")
	}
	rootCert, err := x509.ParseCertificate(rootBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parsing root certificate: %w", err)
	}

	// Build verification pools
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)

	intermediates := x509.NewCertPool()
	intermediates.AddCert(ica.Certificate)

	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   time.Now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}

	if _, err := cert.Verify(opts); err != nil {
		return fmt.Errorf("certificate verification failed: %w", err)
	}

	return nil
}

// GetRootCertificate returns the root CA certificate
func (ica *IntermediateCA) GetRootCertificate() (*x509.Certificate, error) {
	block, _ := pem.Decode(ica.RootCertPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode root certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// helper functions

func generateSerialNumber() (*big.Int, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialNumberLimit)
}

func marshalEd25519PrivateKey(key ed25519.PrivateKey) ([]byte, error) {
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8,
	}), nil
}

func parseEd25519PrivateKey(der []byte) (ed25519.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not Ed25519")
	}
	return edKey, nil
}

func computeThumbprint(certDER []byte) string {
	hash := crypto.SHA256.New()
	hash.Write(certDER)
	return fmt.Sprintf("%x", hash.Sum(nil))[:16]
}

func ipStrings(ips []net.IP) []string {
	result := make([]string, len(ips))
	for i, ip := range ips {
		result[i] = ip.String()
	}
	return result
}

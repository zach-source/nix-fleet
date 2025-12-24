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

// CertRequest holds parameters for issuing a host certificate
type CertRequest struct {
	Hostname string
	SANs     []string      // Additional DNS names and IP addresses
	Validity time.Duration // Certificate validity period
}

// IssuedCert represents an issued certificate with its key material
type IssuedCert struct {
	CertPEM    []byte
	KeyPEM     []byte
	Hostname   string
	Serial     string
	NotBefore  time.Time
	NotAfter   time.Time
	SANs       []string
	Thumbprint string
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

	return &IssuedCert{
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		Hostname:   req.Hostname,
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

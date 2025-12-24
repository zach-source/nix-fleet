package pki

import (
	"testing"
	"time"
)

func TestInitCA(t *testing.T) {
	cfg := &CAConfig{
		CommonName:   "Test CA",
		Organization: "Test Org",
		Validity:     24 * time.Hour,
	}

	ca, err := InitCA(cfg)
	if err != nil {
		t.Fatalf("InitCA failed: %v", err)
	}

	if ca.Certificate == nil {
		t.Error("Certificate is nil")
	}
	if ca.PrivateKey == nil {
		t.Error("PrivateKey is nil")
	}
	if len(ca.CertPEM) == 0 {
		t.Error("CertPEM is empty")
	}
	if len(ca.KeyPEM) == 0 {
		t.Error("KeyPEM is empty")
	}

	// Verify certificate properties
	if ca.Certificate.Subject.CommonName != "Test CA" {
		t.Errorf("Expected CN 'Test CA', got '%s'", ca.Certificate.Subject.CommonName)
	}
	if !ca.Certificate.IsCA {
		t.Error("Certificate should be a CA")
	}
}

func TestIssueCert(t *testing.T) {
	// Create CA
	caCfg := &CAConfig{
		CommonName:   "Test CA",
		Organization: "Test Org",
		Validity:     365 * 24 * time.Hour,
	}

	ca, err := InitCA(caCfg)
	if err != nil {
		t.Fatalf("InitCA failed: %v", err)
	}

	// Issue host cert
	req := &CertRequest{
		Hostname: "test-host",
		SANs:     []string{"test-host.local", "192.168.1.100"},
		Validity: 30 * 24 * time.Hour,
	}

	cert, err := ca.IssueCert(req)
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}

	if cert.Hostname != "test-host" {
		t.Errorf("Expected hostname 'test-host', got '%s'", cert.Hostname)
	}
	if len(cert.CertPEM) == 0 {
		t.Error("CertPEM is empty")
	}
	if len(cert.KeyPEM) == 0 {
		t.Error("KeyPEM is empty")
	}

	// Verify SANs include hostname and additional SANs
	expectedSANs := map[string]bool{
		"test-host":       true,
		"test-host.local": true,
		"192.168.1.100":   true,
	}
	for _, san := range cert.SANs {
		if !expectedSANs[san] {
			t.Errorf("Unexpected SAN: %s", san)
		}
		delete(expectedSANs, san)
	}
	for san := range expectedSANs {
		t.Errorf("Missing SAN: %s", san)
	}
}

func TestVerifyCert(t *testing.T) {
	// Create CA
	caCfg := &CAConfig{
		CommonName:   "Test CA",
		Organization: "Test Org",
		Validity:     365 * 24 * time.Hour,
	}

	ca, err := InitCA(caCfg)
	if err != nil {
		t.Fatalf("InitCA failed: %v", err)
	}

	// Issue cert
	req := &CertRequest{
		Hostname: "test-host",
		Validity: 30 * 24 * time.Hour,
	}

	cert, err := ca.IssueCert(req)
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}

	// Verify should pass
	if err := ca.Verify(cert.CertPEM); err != nil {
		t.Errorf("Verify failed: %v", err)
	}
}

func TestLoadCA(t *testing.T) {
	// Create CA
	caCfg := &CAConfig{
		CommonName:   "Test CA",
		Organization: "Test Org",
		Validity:     365 * 24 * time.Hour,
	}

	ca, err := InitCA(caCfg)
	if err != nil {
		t.Fatalf("InitCA failed: %v", err)
	}

	// Load from PEM
	loaded, err := LoadCA(ca.CertPEM, ca.KeyPEM)
	if err != nil {
		t.Fatalf("LoadCA failed: %v", err)
	}

	if loaded.Certificate.Subject.CommonName != ca.Certificate.Subject.CommonName {
		t.Error("Loaded CA has different CN")
	}
}

func TestParseCertInfo(t *testing.T) {
	// Create CA
	caCfg := DefaultCAConfig()
	ca, err := InitCA(caCfg)
	if err != nil {
		t.Fatalf("InitCA failed: %v", err)
	}

	// Issue cert
	req := &CertRequest{
		Hostname: "info-test",
		SANs:     []string{"192.168.1.1"},
		Validity: 365 * 24 * time.Hour,
	}

	cert, err := ca.IssueCert(req)
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}

	// Parse info
	info, err := ParseCertInfo(cert.CertPEM)
	if err != nil {
		t.Fatalf("ParseCertInfo failed: %v", err)
	}

	if info.Hostname != "info-test" {
		t.Errorf("Expected hostname 'info-test', got '%s'", info.Hostname)
	}
	if info.Status != "valid" {
		t.Errorf("Expected status 'valid', got '%s'", info.Status)
	}
	if info.DaysLeft < 364 || info.DaysLeft > 366 {
		t.Errorf("Expected ~365 days left, got %d", info.DaysLeft)
	}
}

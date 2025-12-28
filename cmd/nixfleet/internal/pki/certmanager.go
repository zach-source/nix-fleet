package pki

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CertManagerConfig configures the cert-manager webhook server
type CertManagerConfig struct {
	ListenAddr      string        // Address to listen on (default: ":8443")
	TLSCertFile     string        // TLS certificate for webhook server
	TLSKeyFile      string        // TLS key for webhook server
	DefaultValidity time.Duration // Default certificate validity
}

// DefaultCertManagerConfig returns default configuration
func DefaultCertManagerConfig() *CertManagerConfig {
	return &CertManagerConfig{
		ListenAddr:      ":8443",
		DefaultValidity: 90 * 24 * time.Hour, // 90 days
	}
}

// CertManagerWebhook handles cert-manager signing requests
type CertManagerWebhook struct {
	ca     *CA
	config *CertManagerConfig
}

// NewCertManagerWebhook creates a new webhook handler
func NewCertManagerWebhook(ca *CA, config *CertManagerConfig) *CertManagerWebhook {
	if config == nil {
		config = DefaultCertManagerConfig()
	}
	return &CertManagerWebhook{
		ca:     ca,
		config: config,
	}
}

// CertManagerSignRequest represents a signing request from cert-manager
type CertManagerSignRequest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Request    string   `json:"request"`    // Base64-encoded CSR
		SignerName string   `json:"signerName"` // e.g., "nixfleet.io/fleet-ca"
		Usages     []string `json:"usages"`
		Duration   string   `json:"duration,omitempty"`
	} `json:"spec"`
}

// CertManagerSignResponse is the webhook response
type CertManagerSignResponse struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Status     struct {
		Certificate string `json:"certificate,omitempty"` // Base64-encoded signed cert
		Conditions  []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason,omitempty"`
			Message string `json:"message,omitempty"`
		} `json:"conditions,omitempty"`
	} `json:"status"`
}

// SignCSR signs a Certificate Signing Request
func (w *CertManagerWebhook) SignCSR(csrPEM []byte, validity time.Duration) ([]byte, error) {
	// Decode CSR
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode CSR PEM")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CSR: %w", err)
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid CSR signature: %w", err)
	}

	// Extract SANs from CSR
	var sans []string
	sans = append(sans, csr.DNSNames...)
	for _, ip := range csr.IPAddresses {
		sans = append(sans, ip.String())
	}

	// Issue certificate using our CA
	req := &CertRequest{
		Hostname: csr.Subject.CommonName,
		SANs:     sans,
		Validity: validity,
	}

	cert, err := w.ca.IssueCert(req)
	if err != nil {
		return nil, fmt.Errorf("issuing certificate: %w", err)
	}

	return cert.CertPEM, nil
}

// ServeHTTP handles webhook requests
func (w *CertManagerWebhook) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(rw, "failed to read request", http.StatusBadRequest)
		return
	}

	var signReq CertManagerSignRequest
	if err := json.Unmarshal(body, &signReq); err != nil {
		http.Error(rw, "invalid request JSON", http.StatusBadRequest)
		return
	}

	// Decode CSR from request
	csrPEM, err := base64.StdEncoding.DecodeString(signReq.Spec.Request)
	if err != nil {
		w.sendError(rw, "failed to decode CSR", err)
		return
	}

	// Parse duration if provided
	validity := w.config.DefaultValidity
	if signReq.Spec.Duration != "" {
		if d, err := time.ParseDuration(signReq.Spec.Duration); err == nil {
			validity = d
		}
	}

	// Sign the CSR
	certPEM, err := w.SignCSR(csrPEM, validity)
	if err != nil {
		w.sendError(rw, "failed to sign CSR", err)
		return
	}

	// Build response
	resp := CertManagerSignResponse{
		APIVersion: "certificates.k8s.io/v1",
		Kind:       "CertificateSigningRequest",
	}
	resp.Status.Certificate = base64.StdEncoding.EncodeToString(certPEM)
	resp.Status.Conditions = []struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Reason  string `json:"reason,omitempty"`
		Message string `json:"message,omitempty"`
	}{
		{
			Type:   "Approved",
			Status: "True",
			Reason: "NixFleetApproved",
		},
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(resp)
}

func (w *CertManagerWebhook) sendError(rw http.ResponseWriter, msg string, err error) {
	resp := CertManagerSignResponse{
		APIVersion: "certificates.k8s.io/v1",
		Kind:       "CertificateSigningRequest",
	}
	resp.Status.Conditions = []struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Reason  string `json:"reason,omitempty"`
		Message string `json:"message,omitempty"`
	}{
		{
			Type:    "Failed",
			Status:  "True",
			Reason:  "SigningFailed",
			Message: fmt.Sprintf("%s: %v", msg, err),
		},
	}

	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(rw).Encode(resp)
}

// KubernetesSecret represents a Kubernetes TLS secret
type KubernetesSecret struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Type       string            `json:"type"`
	Metadata   map[string]any    `json:"metadata"`
	Data       map[string]string `json:"data"`
}

// ExportToK8sSecret exports a certificate as a Kubernetes TLS secret
// If the certificate has a chain (from intermediate CA), the full chain is exported
func ExportToK8sSecret(cert *IssuedCert, caCert []byte, namespace, secretName string) (*KubernetesSecret, error) {
	if namespace == "" {
		namespace = "default"
	}
	if secretName == "" {
		secretName = cert.Hostname + "-tls"
		if cert.Name != "" && cert.Name != "host" {
			secretName = cert.Hostname + "-" + cert.Name + "-tls"
		}
	}

	// Use chain if available (cert + intermediate + root), otherwise just the cert
	certData := cert.CertPEM
	if len(cert.ChainPEM) > 0 {
		certData = cert.ChainPEM
	}

	secret := &KubernetesSecret{
		APIVersion: "v1",
		Kind:       "Secret",
		Type:       "kubernetes.io/tls",
		Metadata: map[string]any{
			"name":      secretName,
			"namespace": namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "nixfleet",
				"nixfleet.io/hostname":         cert.Hostname,
			},
			"annotations": map[string]string{
				"nixfleet.io/cert-name":  cert.Name,
				"nixfleet.io/serial":     cert.Serial,
				"nixfleet.io/expires":    cert.NotAfter.Format(time.RFC3339),
				"nixfleet.io/thumbprint": cert.Thumbprint,
				"nixfleet.io/has-chain":  fmt.Sprintf("%t", len(cert.ChainPEM) > 0),
			},
		},
		Data: map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(certData),
			"tls.key": base64.StdEncoding.EncodeToString(cert.KeyPEM),
		},
	}

	// Add CA certificate if provided
	if len(caCert) > 0 {
		secret.Data["ca.crt"] = base64.StdEncoding.EncodeToString(caCert)
	}

	return secret, nil
}

// ExportCAToK8sSecret exports the CA certificate as a Kubernetes secret
// This can be used with cert-manager's CA issuer
func ExportCAToK8sSecret(ca *CA, namespace, secretName string) (*KubernetesSecret, error) {
	if namespace == "" {
		namespace = "cert-manager"
	}
	if secretName == "" {
		secretName = "nixfleet-ca"
	}

	secret := &KubernetesSecret{
		APIVersion: "v1",
		Kind:       "Secret",
		Type:       "kubernetes.io/tls",
		Metadata: map[string]any{
			"name":      secretName,
			"namespace": namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "nixfleet",
				"nixfleet.io/ca":               "true",
			},
			"annotations": map[string]string{
				"nixfleet.io/expires": ca.Certificate.NotAfter.Format(time.RFC3339),
			},
		},
		Data: map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(ca.CertPEM),
			"tls.key": base64.StdEncoding.EncodeToString(ca.KeyPEM),
		},
	}

	return secret, nil
}

// ExportIntermediateCAToK8sSecret exports the intermediate CA as a Kubernetes secret
// The secret includes the full chain (intermediate + root) for proper validation
func ExportIntermediateCAToK8sSecret(ica *IntermediateCA, namespace, secretName string) (*KubernetesSecret, error) {
	if namespace == "" {
		namespace = "cert-manager"
	}
	if secretName == "" {
		secretName = "nixfleet-ca"
	}

	secret := &KubernetesSecret{
		APIVersion: "v1",
		Kind:       "Secret",
		Type:       "kubernetes.io/tls",
		Metadata: map[string]any{
			"name":      secretName,
			"namespace": namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "nixfleet",
				"nixfleet.io/ca":               "true",
				"nixfleet.io/intermediate":     "true",
			},
			"annotations": map[string]string{
				"nixfleet.io/expires": ica.Certificate.NotAfter.Format(time.RFC3339),
			},
		},
		Data: map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(ica.CertPEM),
			"tls.key": base64.StdEncoding.EncodeToString(ica.KeyPEM),
			"ca.crt":  base64.StdEncoding.EncodeToString(ica.ChainPEM), // Full chain for trust
		},
	}

	return secret, nil
}

// CertManagerIssuer represents a cert-manager ClusterIssuer
type CertManagerIssuer struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   map[string]any `json:"metadata"`
	Spec       map[string]any `json:"spec"`
}

// GenerateCertManagerIssuer generates a cert-manager CA issuer configuration
func GenerateCertManagerIssuer(secretName, secretNamespace, issuerName string) *CertManagerIssuer {
	if issuerName == "" {
		issuerName = "nixfleet-ca-issuer"
	}

	return &CertManagerIssuer{
		APIVersion: "cert-manager.io/v1",
		Kind:       "ClusterIssuer",
		Metadata: map[string]any{
			"name": issuerName,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "nixfleet",
			},
		},
		Spec: map[string]any{
			"ca": map[string]any{
				"secretName": secretName,
			},
		},
	}
}

// HostCertSpec defines a certificate specification for a host
// Used for configuration files and multi-cert management
type HostCertSpec struct {
	Name        string           `json:"name"`               // Certificate name
	SANs        []string         `json:"sans,omitempty"`     // Subject Alternative Names
	Validity    string           `json:"validity,omitempty"` // Duration string (e.g., "365d")
	InstallSpec *CertInstallSpec `json:"install,omitempty"`  // Installation configuration
}

// HostCertsConfig defines all certificates for a host
type HostCertsConfig struct {
	Hostname     string          `json:"hostname"`
	Certificates []*HostCertSpec `json:"certificates"`
}

// LoadHostCertsConfig loads certificate configuration for a host
func LoadHostCertsConfig(configPath string) (*HostCertsConfig, error) {
	// This would load from a JSON/YAML file
	// For now, return nil to indicate no config file
	return nil, nil
}

// ParseValidityDuration parses a validity duration string (e.g., "90d", "1y")
func ParseValidityDuration(s string) (time.Duration, error) {
	if s == "" {
		return 365 * 24 * time.Hour, nil // Default 1 year
	}

	s = strings.TrimSpace(strings.ToLower(s))

	// Handle special suffixes
	if strings.HasSuffix(s, "d") {
		days := strings.TrimSuffix(s, "d")
		var d int
		if _, err := fmt.Sscanf(days, "%d", &d); err != nil {
			return 0, fmt.Errorf("invalid days format: %s", s)
		}
		return time.Duration(d) * 24 * time.Hour, nil
	}

	if strings.HasSuffix(s, "y") {
		years := strings.TrimSuffix(s, "y")
		var y int
		if _, err := fmt.Sscanf(years, "%d", &y); err != nil {
			return 0, fmt.Errorf("invalid years format: %s", s)
		}
		return time.Duration(y) * 365 * 24 * time.Hour, nil
	}

	// Fall back to standard duration parsing
	return time.ParseDuration(s)
}

// StartWebhookServer starts the cert-manager webhook server
func (w *CertManagerWebhook) StartServer(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/sign", w)
	mux.HandleFunc("/health", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:    w.config.ListenAddr,
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if w.config.TLSCertFile != "" && w.config.TLSKeyFile != "" {
		return server.ListenAndServeTLS(w.config.TLSCertFile, w.config.TLSKeyFile)
	}

	return server.ListenAndServe()
}

package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultSignerName is the spec.signerName a request must carry.
const DefaultSignerName = "nixfleet.io/fleet-ca"

// CertManagerConfig configures the cert-manager webhook server
type CertManagerConfig struct {
	ListenAddr   string // Address to listen on (default: "127.0.0.1:8443")
	TLSCertFile  string // TLS certificate for webhook server
	TLSKeyFile   string // TLS key for webhook server
	ClientCAFile string // If set, clients must present a cert issued by this CA (mTLS)

	Token      string // Bearer token callers must present
	SignerName string // Required spec.signerName (default: DefaultSignerName)

	// AllowedDNS lists the DNS names this webhook may sign. An entry may be an
	// exact name or a "*.example.com" wildcard matching exactly one label.
	AllowedDNS []string
	// AllowedIPCIDRs lists the CIDRs an IP SAN must fall inside.
	AllowedIPCIDRs []string

	DefaultValidity time.Duration // Validity used when the request omits one
	MaxValidity     time.Duration // Upper bound on a requested validity
}

// DefaultCertManagerConfig returns default configuration
func DefaultCertManagerConfig() *CertManagerConfig {
	return &CertManagerConfig{
		ListenAddr:      "127.0.0.1:8443",
		SignerName:      DefaultSignerName,
		DefaultValidity: 90 * 24 * time.Hour, // 90 days
		MaxValidity:     90 * 24 * time.Hour,
	}
}

// Validate reports why the webhook must not start. It signs with the Fleet CA,
// so it refuses every configuration that would let an unidentified caller, or
// any caller at all, obtain a certificate for a name of their choosing.
func (c *CertManagerConfig) Validate() error {
	if c.Token == "" && c.ClientCAFile == "" && !isLoopbackAddr(c.ListenAddr) {
		return fmt.Errorf("refusing to listen on %s without caller authentication: set --token-file (or NIXFLEET_WEBHOOK_TOKEN), or --client-ca for mTLS, or bind to 127.0.0.1", c.ListenAddr)
	}
	if len(c.AllowedDNS) == 0 && len(c.AllowedIPCIDRs) == 0 {
		return fmt.Errorf("refusing to start: with no name constraints the webhook would sign a certificate for any name; pass --allow-dns and/or --allow-ip")
	}
	for _, cidr := range c.AllowedIPCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid --allow-ip %q: %w", cidr, err)
		}
	}
	if c.MaxValidity > 0 && c.DefaultValidity > c.MaxValidity {
		return fmt.Errorf("default validity %s exceeds max validity %s", c.DefaultValidity, c.MaxValidity)
	}
	return nil
}

// isLoopbackAddr reports whether a listen address only accepts local connections.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	if config.SignerName == "" {
		config.SignerName = DefaultSignerName
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

// maxCSRBytes caps the request body. A PEM CSR is a couple of kilobytes.
const maxCSRBytes = 64 << 10

// errUnauthorizedCSR marks a rejection caused by the request rather than by a
// failure on our side, so ServeHTTP can answer 403 instead of 500.
type errUnauthorizedCSR struct{ error }

func rejectCSR(format string, args ...any) error {
	return errUnauthorizedCSR{fmt.Errorf(format, args...)}
}

// SignCSR validates a Certificate Signing Request against the configured
// constraints and, if it passes, signs the requester's public key.
func (w *CertManagerWebhook) SignCSR(csrPEM []byte, validity time.Duration) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, rejectCSR("failed to decode CSR PEM")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, rejectCSR("parsing CSR: %v", err)
	}

	// Proof of possession: the requester holds the key they are asking us to
	// certify. The signed certificate below binds that same key.
	if err := csr.CheckSignature(); err != nil {
		return nil, rejectCSR("invalid CSR signature: %v", err)
	}

	if err := w.checkCSR(csr); err != nil {
		return nil, err
	}

	if validity <= 0 {
		validity = w.config.DefaultValidity
	}
	if max := w.config.MaxValidity; max > 0 && validity > max {
		validity = max
	}

	return w.ca.signCSRCert(csr, validity)
}

// checkCSR applies the authorization rules: no CA certificates, no key types
// or sizes we consider weak, no SAN we were not configured to sign, and no SAN
// type we would silently drop.
func (w *CertManagerWebhook) checkCSR(csr *x509.CertificateRequest) error {
	if len(csr.EmailAddresses) > 0 || len(csr.URIs) > 0 {
		return rejectCSR("CSR requests email/URI SANs, which this signer does not issue")
	}
	if len(csr.DNSNames) == 0 && len(csr.IPAddresses) == 0 && csr.Subject.CommonName == "" {
		return rejectCSR("CSR names no subject: set a CommonName or at least one SAN")
	}
	if requestsCA(csr) {
		return rejectCSR("CSR requests a CA certificate")
	}
	if err := checkKeyStrength(csr); err != nil {
		return err
	}

	// The CommonName ends up in the issued certificate's subject, so it is
	// constrained like any other name.
	if cn := csr.Subject.CommonName; cn != "" && net.ParseIP(cn) == nil {
		if !dnsNameAllowed(cn, w.config.AllowedDNS) {
			return rejectCSR("CommonName %q is not permitted by --allow-dns", cn)
		}
	} else if cn != "" {
		if !ipAllowed(net.ParseIP(cn), w.config.AllowedIPCIDRs) {
			return rejectCSR("CommonName %q is not permitted by --allow-ip", cn)
		}
	}
	for _, name := range csr.DNSNames {
		if !dnsNameAllowed(name, w.config.AllowedDNS) {
			return rejectCSR("DNS name %q is not permitted by --allow-dns", name)
		}
	}
	for _, ip := range csr.IPAddresses {
		if !ipAllowed(ip, w.config.AllowedIPCIDRs) {
			return rejectCSR("IP address %q is not permitted by --allow-ip", ip)
		}
	}
	return nil
}

// oidBasicConstraints is the basicConstraints extension (RFC 5280 4.2.1.9).
var oidBasicConstraints = asn1.ObjectIdentifier{2, 5, 29, 19}

// requestsCA reports whether the CSR asks for basicConstraints CA:TRUE. Our
// template never sets IsCA, but refuse the request outright rather than
// silently issuing something other than what was asked for.
func requestsCA(csr *x509.CertificateRequest) bool {
	for _, ext := range csr.Extensions {
		if !ext.Id.Equal(oidBasicConstraints) {
			continue
		}
		var bc struct {
			IsCA       bool `asn1:"optional"`
			MaxPathLen int  `asn1:"optional"`
		}
		if _, err := asn1.Unmarshal(ext.Value, &bc); err != nil {
			return true // unparseable constraints: don't guess, reject
		}
		if bc.IsCA {
			return true
		}
	}
	// ExtraExtensions is populated when a CSR is built in-process; cert-manager
	// sends DER, so Extensions is the field that matters. Check both anyway.
	for _, ext := range csr.ExtraExtensions {
		if ext.Id.Equal(oidBasicConstraints) {
			return true
		}
	}
	return false
}

func checkKeyStrength(csr *x509.CertificateRequest) error {
	switch pub := csr.PublicKey.(type) {
	case *rsa.PublicKey:
		if pub.N.BitLen() < 2048 {
			return rejectCSR("RSA key is %d bits, minimum is 2048", pub.N.BitLen())
		}
	case *ecdsa.PublicKey:
		if pub.Curve.Params().BitSize < 256 {
			return rejectCSR("ECDSA curve %s is below P-256", pub.Curve.Params().Name)
		}
	case ed25519.PublicKey:
	default:
		return rejectCSR("unsupported public key type %T", csr.PublicKey)
	}
	return nil
}

// dnsNameAllowed matches name against exact entries and "*.example.com"
// patterns, where the wildcard stands for exactly one label.
func dnsNameAllowed(name string, allowed []string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" {
		return false
	}
	for _, pattern := range allowed {
		pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
		if pattern == name {
			return true
		}
		suffix, ok := strings.CutPrefix(pattern, "*.")
		if !ok {
			continue
		}
		label, rest, found := strings.Cut(name, ".")
		if found && label != "" && label != "*" && rest == suffix {
			return true
		}
	}
	return false
}

func ipAllowed(ip net.IP, cidrs []string) bool {
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		_, network, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// authenticate reports whether the caller proved it may use this signer. mTLS
// is enforced by the TLS handshake when ClientCAFile is set; here we only check
// the bearer token.
func (w *CertManagerWebhook) authenticate(r *http.Request) bool {
	if w.config.Token == "" {
		return true
	}
	expected := "Bearer " + w.config.Token
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) == 1
}

// ServeHTTP handles webhook requests
func (w *CertManagerWebhook) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !w.authenticate(r) {
		rw.Header().Set("WWW-Authenticate", `Bearer realm="nixfleet-pki"`)
		http.Error(rw, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxCSRBytes+1))
	if err != nil {
		http.Error(rw, "failed to read request", http.StatusBadRequest)
		return
	}
	if len(body) > maxCSRBytes {
		http.Error(rw, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	var signReq CertManagerSignRequest
	if err := json.Unmarshal(body, &signReq); err != nil {
		http.Error(rw, "invalid request JSON", http.StatusBadRequest)
		return
	}

	// An authenticated caller is still only authorized for this signer.
	if signReq.Spec.SignerName != w.config.SignerName {
		w.sendError(rw, http.StatusForbidden, "unknown signerName",
			fmt.Errorf("%q, this webhook signs for %q", signReq.Spec.SignerName, w.config.SignerName))
		return
	}

	csrPEM, err := base64.StdEncoding.DecodeString(signReq.Spec.Request)
	if err != nil {
		w.sendError(rw, http.StatusBadRequest, "failed to decode CSR", err)
		return
	}

	// Parse duration if provided
	validity := w.config.DefaultValidity
	if signReq.Spec.Duration != "" {
		d, err := time.ParseDuration(signReq.Spec.Duration)
		if err != nil {
			w.sendError(rw, http.StatusBadRequest, "invalid duration", err)
			return
		}
		validity = d
	}

	certPEM, err := w.SignCSR(csrPEM, validity)
	if err != nil {
		status := http.StatusInternalServerError
		var rejected errUnauthorizedCSR
		if errors.As(err, &rejected) {
			status = http.StatusForbidden
		}
		w.sendError(rw, status, "failed to sign CSR", err)
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

func (w *CertManagerWebhook) sendError(rw http.ResponseWriter, status int, msg string, err error) {
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
	rw.WriteHeader(status)
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
	if err := w.config.Validate(); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/sign", w)
	mux.HandleFunc("/health", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              w.config.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if w.config.ClientCAFile != "" {
		pool, err := loadClientCAPool(w.config.ClientCAFile)
		if err != nil {
			return err
		}
		server.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ClientCAs:  pool,
			ClientAuth: tls.RequireAndVerifyClientCert,
		}
		if w.config.TLSCertFile == "" || w.config.TLSKeyFile == "" {
			return fmt.Errorf("--client-ca requires --tls-cert and --tls-key")
		}
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

func loadClientCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client CA %s contains no certificates", path)
	}
	return pool, nil
}

// signCSRCert issues a leaf certificate over the CSR's own public key.
//
// IssueCert generates a fresh key pair and certifies that instead, which is
// right for `nixfleet pki issue` (it hands back the key too) but wrong here:
// cert-manager holds the key, proved it in the CSR signature, and cannot use a
// certificate for some other key. Only names and validity are taken from the
// CSR; key usage and basic constraints come from us.
func (ca *CA) signCSRCert(csr *x509.CertificateRequest, validity time.Duration) ([]byte, error) {
	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   csr.Subject.CommonName,
			Organization: ca.Certificate.Subject.Organization,
		},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              csr.DNSNames,
		IPAddresses:           csr.IPAddresses,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Certificate, csr.PublicKey, ca.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("signing certificate: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), nil
}

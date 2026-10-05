package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testCA(t *testing.T) *CA {
	t.Helper()
	ca, err := InitCA(&CAConfig{
		CommonName:   "Test Fleet CA",
		Organization: "Test",
		Validity:     24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

type csrOpts struct {
	cn    string
	dns   []string
	ips   []net.IP
	email []string
	isCA  bool
	rsa   int // bits; 0 means ECDSA P-256
}

func makeCSR(t *testing.T, o csrOpts) ([]byte, any) {
	t.Helper()

	var key any
	var err error
	if o.rsa > 0 {
		key, err = rsa.GenerateKey(rand.Reader, o.rsa)
	} else {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.CertificateRequest{
		Subject:        pkix.Name{CommonName: o.cn},
		DNSNames:       o.dns,
		IPAddresses:    o.ips,
		EmailAddresses: o.email,
	}
	if o.isCA {
		value, err := asn1.Marshal(struct {
			IsCA       bool `asn1:"optional"`
			MaxPathLen int  `asn1:"optional,default:-1"`
		}{IsCA: true, MaxPathLen: 1})
		if err != nil {
			t.Fatal(err)
		}
		tmpl.ExtraExtensions = []pkix.Extension{{Id: oidBasicConstraints, Critical: true, Value: value}}
	}

	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), key
}

func testWebhook(t *testing.T) *CertManagerWebhook {
	t.Helper()
	cfg := DefaultCertManagerConfig()
	cfg.AllowedDNS = []string{"host.example.com", "*.svc.cluster.local"}
	cfg.AllowedIPCIDRs = []string{"10.1.0.0/16"}
	cfg.MaxValidity = 48 * time.Hour
	cfg.DefaultValidity = 24 * time.Hour
	return NewCertManagerWebhook(testCA(t), cfg)
}

// The signed certificate must certify the key the requester proved possession
// of. Issuing over a server-generated key leaves cert-manager with a
// certificate it cannot use.
func TestSignCSRBindsTheRequestersKey(t *testing.T) {
	w := testWebhook(t)
	csrPEM, key := makeCSR(t, csrOpts{cn: "host.example.com", dns: []string{"host.example.com"}})

	certPEM, err := w.SignCSR(csrPEM, 0)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	want := key.(*ecdsa.PrivateKey).Public().(*ecdsa.PublicKey)
	got, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("certificate key type = %T, want *ecdsa.PublicKey", cert.PublicKey)
	}
	if !got.Equal(want) {
		t.Error("certificate does not certify the CSR's public key")
	}
	if cert.IsCA {
		t.Error("issued a CA certificate")
	}
	if err := w.ca.Verify(certPEM); err != nil {
		t.Errorf("certificate does not chain to the CA: %v", err)
	}
}

func TestSignCSRRejectsNamesOutsideThePolicy(t *testing.T) {
	w := testWebhook(t)

	cases := []struct {
		name string
		opts csrOpts
	}{
		{"unlisted DNS name", csrOpts{cn: "evil.example.com", dns: []string{"evil.example.com"}}},
		{"CN inside, SAN outside", csrOpts{cn: "host.example.com", dns: []string{"host.example.com", "evil.example.com"}}},
		{"wildcard crosses a label", csrOpts{dns: []string{"a.b.svc.cluster.local"}}},
		{"wildcard requested literally", csrOpts{dns: []string{"*.example.com"}}},
		{"parent of a wildcard", csrOpts{dns: []string{"svc.cluster.local"}}},
		{"IP outside the CIDR", csrOpts{ips: []net.IP{net.ParseIP("192.168.1.5")}}},
		{"email SAN", csrOpts{cn: "host.example.com", email: []string{"root@example.com"}}},
		{"no names at all", csrOpts{}},
		{"CA requested", csrOpts{cn: "host.example.com", dns: []string{"host.example.com"}, isCA: true}},
		{"weak RSA key", csrOpts{cn: "host.example.com", dns: []string{"host.example.com"}, rsa: 1024}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			csrPEM, _ := makeCSR(t, c.opts)
			if _, err := w.SignCSR(csrPEM, 0); err == nil {
				t.Fatal("expected a rejection, got a signed certificate")
			}
		})
	}
}

func TestSignCSRAcceptsWildcardAndIPWithinPolicy(t *testing.T) {
	w := testWebhook(t)

	for _, o := range []csrOpts{
		{dns: []string{"pod.svc.cluster.local"}},
		{cn: "host.example.com", ips: []net.IP{net.ParseIP("10.1.2.3")}},
		{cn: "host.example.com", dns: []string{"host.example.com"}, rsa: 2048},
	} {
		csrPEM, _ := makeCSR(t, o)
		if _, err := w.SignCSR(csrPEM, 0); err != nil {
			t.Errorf("SignCSR(%+v) = %v, want success", o, err)
		}
	}
}

func TestSignCSRClampsValidity(t *testing.T) {
	w := testWebhook(t)
	csrPEM, _ := makeCSR(t, csrOpts{cn: "host.example.com", dns: []string{"host.example.com"}})

	certPEM, err := w.SignCSR(csrPEM, 10*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if lifetime := cert.NotAfter.Sub(cert.NotBefore); lifetime > w.config.MaxValidity {
		t.Errorf("lifetime %s exceeds the %s cap", lifetime, w.config.MaxValidity)
	}
}

func signRequest(t *testing.T, csrPEM []byte, signerName string) *strings.Reader {
	t.Helper()
	var req CertManagerSignRequest
	req.APIVersion = "certificates.k8s.io/v1"
	req.Kind = "CertificateSigningRequest"
	req.Spec.Request = base64.StdEncoding.EncodeToString(csrPEM)
	req.Spec.SignerName = signerName
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(body))
}

func TestServeHTTPRequiresTheToken(t *testing.T) {
	w := testWebhook(t)
	w.config.Token = "correct-horse"
	csrPEM, _ := makeCSR(t, csrOpts{cn: "host.example.com", dns: []string{"host.example.com"}})

	for _, c := range []struct {
		name, header string
		want         int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong", http.StatusUnauthorized},
		{"bare token", "correct-horse", http.StatusUnauthorized},
		{"correct token", "Bearer correct-horse", http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/sign", signRequest(t, csrPEM, DefaultSignerName))
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			w.ServeHTTP(rec, r)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d (%s)", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestServeHTTPRejectsAnotherSigner(t *testing.T) {
	w := testWebhook(t)
	csrPEM, _ := makeCSR(t, csrOpts{cn: "host.example.com", dns: []string{"host.example.com"}})

	r := httptest.NewRequest(http.MethodPost, "/sign", signRequest(t, csrPEM, "example.com/other-ca"))
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"Approved"`) {
		t.Error("approved a request for a signer we do not serve")
	}
}

// A disallowed CSR is the caller's fault, not ours: 403, not 500.
func TestServeHTTPReportsRejectionAsForbidden(t *testing.T) {
	w := testWebhook(t)
	csrPEM, _ := makeCSR(t, csrOpts{cn: "evil.example.com", dns: []string{"evil.example.com"}})

	r := httptest.NewRequest(http.MethodPost, "/sign", signRequest(t, csrPEM, DefaultSignerName))
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*CertManagerConfig)
		wantErr string
	}{
		{
			name:    "no name constraints",
			mutate:  func(c *CertManagerConfig) {},
			wantErr: "any name",
		},
		{
			name: "off loopback without auth",
			mutate: func(c *CertManagerConfig) {
				c.ListenAddr = ":8443"
				c.AllowedDNS = []string{"host.example.com"}
			},
			wantErr: "caller authentication",
		},
		{
			name: "off loopback with a token",
			mutate: func(c *CertManagerConfig) {
				c.ListenAddr = ":8443"
				c.Token = "t"
				c.AllowedDNS = []string{"host.example.com"}
			},
		},
		{
			name: "off loopback with mTLS",
			mutate: func(c *CertManagerConfig) {
				c.ListenAddr = "0.0.0.0:8443"
				c.ClientCAFile = "ca.crt"
				c.AllowedDNS = []string{"host.example.com"}
			},
		},
		{
			name: "loopback with constraints",
			mutate: func(c *CertManagerConfig) {
				c.AllowedIPCIDRs = []string{"127.0.0.0/8"}
			},
		},
		{
			name: "bad CIDR",
			mutate: func(c *CertManagerConfig) {
				c.AllowedIPCIDRs = []string{"10.1.0.0/64"}
			},
			wantErr: "invalid --allow-ip",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := DefaultCertManagerConfig()
			c.mutate(cfg)
			err := cfg.Validate()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case c.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error about %q", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("Validate() = %v, want an error about %q", err, c.wantErr)
			}
		})
	}
}

// Signing anything at all must take an explicit decision, so a zero-value
// config has to be rejected before the listener opens.
func TestStartServerRefusesAnUnconstrainedConfig(t *testing.T) {
	w := NewCertManagerWebhook(testCA(t), DefaultCertManagerConfig())
	if err := w.StartServer(t.Context()); err == nil {
		t.Fatal("StartServer() = nil, want a refusal")
	}
}

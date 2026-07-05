package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// signChild issues a cert (CA or leaf) signed by parent, for simulating the
// part of the chain SPIRE mints itself (its own server CA, then SVIDs).
func signChild(t *testing.T, cn string, isCA bool, pathlen int, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		MaxPathLen:            pathlen,
		MaxPathLenZero:        isCA && pathlen == 0,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &k.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c, k
}

// TestFleetCAPathlenSupportsSPIRE proves the pathlen:2 root lets a SPIRE
// upstream intermediate (pathlen:1) parent SPIRE's own CA down to a leaf SVID.
// This is the exact chain that failed ("too many intermediates for path length
// constraint") when the root was pathlen:1 / intermediate pathlen:0.
func TestFleetCAPathlenSupportsSPIRE(t *testing.T) {
	root, err := InitCA(&CAConfig{CommonName: "Test Root", Organization: "T", Validity: 720 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if root.Certificate.MaxPathLen != 2 {
		t.Fatalf("root MaxPathLen = %d, want 2", root.Certificate.MaxPathLen)
	}

	spireInt, err := root.InitIntermediateCA(&IntermediateCAConfig{
		CommonName: "SPIRE Upstream", Organization: "T", Validity: time.Hour, MaxPathLen: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spireInt.Certificate.MaxPathLen != 1 {
		t.Fatalf("SPIRE intermediate MaxPathLen = %d, want 1", spireInt.Certificate.MaxPathLen)
	}

	// SPIRE mints its own server CA under the upstream, then a leaf SVID.
	spireCA, spireCAKey := signChild(t, "SPIRE Server CA", true, 0, spireInt.Certificate, spireInt.PrivateKey)
	leaf, _ := signChild(t, "workload-svid", false, 0, spireCA, spireCAKey)

	roots := x509.NewCertPool()
	roots.AddCert(root.Certificate)
	inter := x509.NewCertPool()
	inter.AddCert(spireInt.Certificate)
	inter.AddCert(spireCA)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inter,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Fatalf("2-intermediate SPIRE chain must validate under pathlen:2 root: %v", err)
	}

	// Default intermediate stays leaf-only (pathlen:0) so gateway certs are unaffected.
	gwInt, err := root.InitIntermediateCA(DefaultIntermediateCAConfig())
	if err != nil {
		t.Fatal(err)
	}
	if gwInt.Certificate.MaxPathLen != 0 || !gwInt.Certificate.MaxPathLenZero {
		t.Fatalf("default intermediate must stay pathlen:0, got %d", gwInt.Certificate.MaxPathLen)
	}
}

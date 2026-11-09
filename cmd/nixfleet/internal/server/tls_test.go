package server

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPIStartTLS(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	cert := fixture.TLS.Certificates[0]
	client := fixture.Client()
	client.Timeout = time.Second
	fixture.Close()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for path, data := range map[string][]byte{
		certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
		keyFile:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Server{config: Config{ListenAddr: addr, TLSCertFile: certFile, TLSKeyFile: keyFile, APIToken: "test-token"}, mux: http.NewServeMux()}
	s.scheduler = NewScheduler(s)
	s.mux.HandleFunc("/", s.authMiddleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get("https://" + addr)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("missing token: %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	req, err := http.NewRequest(http.MethodGet, "https://"+addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("valid token: %d", resp.StatusCode)
	}
	resp, err = http.Get("http://" + addr)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			t.Fatal("accepted plaintext")
		}
	}
}

func TestAPIInvalidTLSFailsClosed(t *testing.T) {
	for _, files := range [][2]string{{"missing", ""}, {"", "missing"}, {"missing", "missing"}} {
		s := &Server{config: Config{ListenAddr: "127.0.0.1:0", TLSCertFile: files[0], TLSKeyFile: files[1]}, mux: http.NewServeMux()}
		s.scheduler = NewScheduler(s)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Start(ctx); err == nil || !strings.Contains(err.Error(), "TLS") {
			t.Fatalf("invalid TLS accepted: %v", err)
		}
	}
}

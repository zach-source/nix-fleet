package nodestatus

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStatusTransportAndAuth(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(strconv.FormatBool(secure), func(t *testing.T) {
			cfg := DefaultConfig()
			dir := t.TempDir()
			cfg.StateDir, cfg.LogFile, cfg.HostRepoPath = dir, filepath.Join(dir, "pull.log"), dir
			if err := os.WriteFile(cfg.LogFile, []byte("private deployment output\n"), 0600); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: time.Second}
			scheme := "http"
			if secure {
				fixture := httptest.NewTLSServer(http.NotFoundHandler())
				cert := fixture.TLS.Certificates[0]
				client = fixture.Client()
				client.Timeout = time.Second
				fixture.Close()
				key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
				if err != nil {
					t.Fatal(err)
				}
				cfg.TLSCertFile, cfg.TLSKeyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
				for path, data := range map[string][]byte{
					cfg.TLSCertFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
					cfg.TLSKeyFile:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
				} {
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				cfg.APITokenFile = filepath.Join(dir, "token")
				if err := os.WriteFile(cfg.APITokenFile, []byte("test-token\n"), 0600); err != nil {
					t.Fatal(err)
				}
				scheme = "https"
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg.BindAddress, cfg.Port = "127.0.0.1", listener.Addr().(*net.TCPAddr).Port
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			base := scheme + "://" + net.JoinHostPort(cfg.BindAddress, strconv.Itoa(cfg.Port))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- NewServer(cfg).Start(ctx) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("shutdown: %v", err)
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for {
				resp, err := client.Get(base + "/health")
				if err == nil {
					resp.Body.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal(err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, path := range []string{"/", "/status", "/pull", "/state", "/health"} {
				for _, token := range []string{"", "wrong-token", "test-token"} {
					req, err := http.NewRequest(http.MethodGet, base+path, nil)
					if err != nil {
						t.Fatal(err)
					}
					if token != "" {
						req.Header.Set("Authorization", "Bearer "+token)
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					want := http.StatusOK
					if secure && path != "/health" && token != "test-token" {
						want = http.StatusUnauthorized
					}
					if resp.StatusCode != want {
						t.Fatalf("%s token=%q: got %d want %d", path, token, resp.StatusCode, want)
					}
					if (want == http.StatusUnauthorized || path == "/health") && strings.Contains(string(body), "private deployment output") {
						t.Fatalf("private log exposed on %s", path)
					}
					if want == http.StatusOK && (path == "/status" || path == "/pull") && !strings.Contains(string(body), "private deployment output") {
						t.Fatalf("authorized log missing on %s", path)
					}
				}
			}
			if secure {
				resp, err := http.Get(strings.Replace(base, "https:", "http:", 1) + "/pull")
				if err == nil {
					defer resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						t.Fatal("TLS server accepted plaintext")
					}
				}
			}
		})
	}
}

func TestInvalidSecurityConfigFailsClosed(t *testing.T) {
	for _, cfg := range []Config{
		{TLSCertFile: "missing"}, {TLSKeyFile: "missing"},
		{TLSCertFile: "missing", TLSKeyFile: "missing"}, {APITokenFile: "missing"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := NewServer(cfg).Start(ctx); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
	empty := filepath.Join(t.TempDir(), "empty-token")
	if err := os.WriteFile(empty, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewServer(Config{APITokenFile: empty}).Start(ctx); err == nil {
		t.Fatal("empty token silently disables authentication")
	}
}

package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nixfleet/nixfleet/internal/synology"
)

func TestSynologyConnectTLSConfig(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":{"sid":"test-session"}}`)
	}))
	defer srv.Close()
	host, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	// The CLI evaluates only this fixture; no Nix or fleet endpoint is invoked.
	if err := os.WriteFile(filepath.Join(dir, "nix"), []byte("#!/bin/sh\nprintf '%s\\n' \"$DSM_TEST_CONFIG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("SYNOLOGY_PASSWORD", "test-password")
	previousFlake := flakePath
	flakePath = dir
	t.Cleanup(func() { flakePath = previousFlake })
	for _, tt := range []struct {
		name    string
		verify  bool
		ca      string
		wantErr bool
	}{
		{"legacy", false, "", false},
		{"untrusted", true, "", true},
		{"trusted CA", true, caFile, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := synology.Config{Enable: true, Host: host, Port: port, HTTPS: true, VerifyTLS: tt.verify, CAFile: tt.ca}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("DSM_TEST_CONFIG", string(raw))
			cl, _, err := synologyConnect(context.Background(), synologyCmd(), "fixture")
			if (err != nil) != tt.wantErr {
				t.Fatalf("connect error = %v, want error %v", err, tt.wantErr)
			}
			if cl != nil {
				cl.Logout(context.Background())
			}
		})
	}
}

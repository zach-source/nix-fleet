package synology

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testClient(t *testing.T, config string) (*Client, error) {
	t.Helper()
	var cfg Config
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		t.Fatal(err)
	}
	return NewClient(cfg, "test password +/&")
}

func TestClientTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":{"sid":"test-session"}}`)
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, options string
		wantErr       bool
	}{
		{"legacy self signed", ``, false},
		{"untrusted certificate", `,"verifyTLS":true`, true},
		{"trusted certificate", fmt.Sprintf(`,"verifyTLS":true,"caFile":%q`, caFile), false},
		{"CA implies verification", fmt.Sprintf(`,"caFile":%q`, caFile), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cl, err := testClient(t, `{"https":true`+tt.options+`}`)
			if err != nil {
				t.Fatal(err)
			}
			cl.baseURL = srv.URL + "/webapi"
			err = cl.Login(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Login error = %v, want error %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				// A trusted CA must still enforce the server identity.
				if tt.options != "" {
					cl.http.Transport.(*http.Transport).TLSClientConfig.ServerName = "wrong.example"
					cl.http.CloseIdleConnections()
					if err := cl.Login(context.Background()); err == nil {
						t.Fatal("accepted wrong server identity")
					}
				}
			}
		})
	}
}

func TestClientTLSInvalidConfig(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalid, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{
		`{"https":false,"verifyTLS":true}`,
		fmt.Sprintf(`{"https":false,"caFile":%q}`, invalid),
		fmt.Sprintf(`{"https":true,"caFile":%q}`, invalid),
		fmt.Sprintf(`{"https":true,"caFile":%q}`, invalid+".missing"),
	} {
		if _, err := testClient(t, cfg); err == nil {
			t.Errorf("accepted invalid TLS config %s", cfg)
		}
	}
}

func TestClientPreservesGETProtocol(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("existing DSM calls must retain GET, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		method := r.Form.Get("method")
		methods = append(methods, method)
		if method == "login" {
			if r.Form.Get("passwd") != "test password +/&" {
				t.Error("password did not survive query encoding")
			}
		} else if method == "list" && r.Form.Get("_sid") != "test-session" {
			t.Error("session missing from API request")
		} else if method == "logout" && r.Form.Has("_sid") {
			t.Error("logout protocol changed")
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"sid":"test-session"}}`)
	}))
	defer srv.Close()
	cl, err := testClient(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	cl.baseURL = srv.URL + "/webapi"
	if err := cl.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Call(context.Background(), "SYNO.Core.Share", "list", 1, map[string]string{"additional": "[\"vol_path\"]"}); err != nil {
		t.Fatal(err)
	}
	cl.Logout(context.Background())
	if strings.Join(methods, ",") != "login,list,logout" || cl.sid != "" {
		t.Fatalf("unexpected session lifecycle: %v", methods)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.DeadlineExceeded
}

func TestClientErrorsDoNotExposeCredentials(t *testing.T) {
	cl, err := testClient(t, `{"host":"127.0.0.1","port":1}`)
	if err != nil {
		t.Fatal(err)
	}
	cl.http.Transport = failingTransport{}
	check := func(err error) {
		t.Helper()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost underlying error: %v", err)
		}
		for _, secret := range []string{cl.passwd, url.QueryEscape(cl.passwd), "test-session", "passwd=", "_sid="} {
			if strings.Contains(err.Error(), secret) {
				t.Error("error exposes credential material")
			}
		}
	}
	check(cl.Login(context.Background()))
	cl.sid = "test-session"
	_, err = cl.Call(context.Background(), "SYNO.Core.Share", "list", 1, nil)
	check(err)
}

func TestClientMalformedURLErrorsDoNotExposeCredentials(t *testing.T) {
	cl, err := testClient(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	cl.baseURL = "http://[::1"
	err = cl.Login(context.Background())
	if err == nil {
		t.Fatal("expected invalid URL error")
	}
	for _, secret := range []string{cl.passwd, url.QueryEscape(cl.passwd), "passwd="} {
		if strings.Contains(err.Error(), secret) {
			t.Error("invalid URL error exposes credentials")
		}
	}
}

func TestClientDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed credential-bearing redirect") }))
			defer destination.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, status) }))
			defer srv.Close()
			cl, err := testClient(t, `{}`)
			if err != nil {
				t.Fatal(err)
			}
			cl.baseURL = srv.URL + "/webapi"
			if err := cl.Login(context.Background()); err == nil {
				t.Fatal("accepted redirect")
			}
		})
	}
}

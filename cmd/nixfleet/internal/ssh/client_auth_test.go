package ssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func newTestSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, priv
}

func writeKeyFile(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// startAgent serves a keyring on a unix socket and sets SSH_AUTH_SOCK.
// Keep the path short: macOS limits sun_path to 104 bytes.
func startAgent(t *testing.T, keys ...ed25519.PrivateKey) {
	t.Helper()
	kr := agent.NewKeyring()
	for _, k := range keys {
		if err := kr.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("", "nfa")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close(); os.RemoveAll(dir) })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { agent.ServeAgent(kr, c); c.Close() }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

// authAgainst runs a handshake with methods against an in-process server that
// accepts only the key `accept`. It returns the client error and the public
// keys the server saw, in order.
func authAgainst(t *testing.T, methods []ssh.AuthMethod, accept ssh.PublicKey) (error, [][]byte) {
	t.Helper()
	hostSigner, _ := newTestSigner(t)
	var mu sync.Mutex
	var offered [][]byte
	scfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			mu.Lock()
			offered = append(offered, k.Marshal())
			mu.Unlock()
			if bytes.Equal(k.Marshal(), accept.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	scfg.AddHostKey(hostSigner)

	// net.Pipe would deadlock: both sides write their version string first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		sc, err := ln.Accept()
		if err != nil {
			return
		}
		_, chans, reqs, err := ssh.NewServerConn(sc, scfg)
		if err != nil {
			sc.Close()
			return
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			for range chans {
			}
		}()
	}()
	cc, derr := net.Dial("tcp", ln.Addr().String())
	if derr != nil {
		t.Fatal(derr)
	}
	conn, _, _, err := ssh.NewClientConn(cc, "x", &ssh.ClientConfig{
		User:            "u",
		Auth:            methods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err == nil {
		conn.Close()
	}
	cc.Close()
	mu.Lock()
	defer mu.Unlock()
	return err, offered
}

func TestAuthFallsBackToAgentWhenFileKeyRejected(t *testing.T) {
	_, fileKey := newTestSigner(t)
	agentSigner, agentKey := newTestSigner(t)
	startAgent(t, agentKey)

	methods, err := buildAuthMethods(&ClientConfig{KeyFiles: []string{writeKeyFile(t, fileKey)}, UseAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	if err, _ := authAgainst(t, methods, agentSigner.PublicKey()); err != nil {
		t.Fatalf("agent key should be offered after the file key is rejected: %v", err)
	}
}

func TestAuthOffersFileKeyBeforeAgentKey(t *testing.T) {
	fileSigner, fileKey := newTestSigner(t)
	_, agentKey := newTestSigner(t)
	startAgent(t, agentKey)

	methods, err := buildAuthMethods(&ClientConfig{KeyFiles: []string{writeKeyFile(t, fileKey)}, UseAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	err, offered := authAgainst(t, methods, fileSigner.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) == 0 || !bytes.Equal(offered[0], fileSigner.PublicKey().Marshal()) {
		t.Fatalf("file key must be offered first, offered %d keys", len(offered))
	}
}

func TestBuildAuthMethodsOneMethod(t *testing.T) {
	_, k1 := newTestSigner(t)
	_, k2 := newTestSigner(t)
	_, k3 := newTestSigner(t)
	startAgent(t, k3)
	methods, err := buildAuthMethods(&ClientConfig{
		KeyFiles: []string{writeKeyFile(t, k1), writeKeyFile(t, k2)},
		UseAgent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 {
		t.Fatalf("got %d methods, want 1", len(methods))
	}
}

func TestAuthDoesNotOfferDuplicateKey(t *testing.T) {
	sA, kA := newTestSigner(t)
	_, kB := newTestSigner(t)
	startAgent(t, kA, kB)

	methods, err := buildAuthMethods(&ClientConfig{KeyFiles: []string{writeKeyFile(t, kA)}, UseAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	// Server accepts nothing useful: every offered key is recorded.
	other, _ := newTestSigner(t)
	_, offered := authAgainst(t, methods, other.PublicKey())
	n := 0
	for _, o := range offered {
		if bytes.Equal(o, sA.PublicKey().Marshal()) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("key A offered %d times, want 1", n)
	}
}

type errAgent struct{ agent.Agent }

func (errAgent) Signers() ([]ssh.Signer, error) { return nil, os.ErrClosed }

func TestCombinedSignersIgnoreAgentError(t *testing.T) {
	s, _ := newTestSigner(t)
	got, err := combinedSigners([]ssh.Signer{s}, errAgent{})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d signers, err %v; want file signer and nil error", len(got), err)
	}
}

func TestCombinedSignersDedupAndOrder(t *testing.T) {
	a, ka := newTestSigner(t)
	b, kb := newTestSigner(t)
	kr := agent.NewKeyring()
	for _, k := range []ed25519.PrivateKey{kb, ka} {
		if err := kr.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := combinedSigners([]ssh.Signer{a}, kr)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d signers, err %v; want 2", len(got), err)
	}
	if !bytes.Equal(got[0].PublicKey().Marshal(), a.PublicKey().Marshal()) ||
		!bytes.Equal(got[1].PublicKey().Marshal(), b.PublicKey().Marshal()) {
		t.Fatal("want file signer first, then the agent-only signer")
	}
}

func TestBuildAuthMethodsNoMethods(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	bad := filepath.Join(t.TempDir(), "garbage")
	if err := os.WriteFile(bad, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []*ClientConfig{
		{KeyFiles: []string{bad, filepath.Join(t.TempDir(), "missing")}, UseAgent: true},
		{UseAgent: false},
	} {
		_, err := buildAuthMethods(cfg)
		if err == nil || !strings.Contains(err.Error(), "no authentication methods available") {
			t.Fatalf("got %v", err)
		}
	}
}

func TestBuildAuthMethodsAgentOffWhenDisabled(t *testing.T) {
	_, k := newTestSigner(t)
	startAgent(t, k)
	if _, err := buildAuthMethods(&ClientConfig{UseAgent: false}); err == nil {
		t.Fatal("agent must not be used when UseAgent is false")
	}
}

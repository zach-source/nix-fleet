package ssh

import (
	"context"
	cryptossh "golang.org/x/crypto/ssh"
	"net"
	"testing"
	"time"
)

func TestIdleCleanupPreservesActiveCommand(t *testing.T) {
	transport := &stalledConn{stage: "start", entered: make(chan struct{}), closed: make(chan struct{}), requests: make(chan *cryptossh.Request)}
	client := &Client{conn: &cryptossh.Client{Conn: transport}, lastUsed: time.Now().Add(-time.Hour)}
	p := testPool(t, func(context.Context, string, int, string) (*Client, error) { return client, nil })
	if _, err := p.Get(context.Background(), "fixture", 22); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = client.Exec(ctx, "fixture") }()
	<-transport.entered
	p.cleanupIdle()
	if !client.IsConnected() {
		t.Error("cleanup closed an active command")
	}
	cancel()
	<-done
}

func TestPoolClosesLateDialAndRejectsNewWork(t *testing.T) {
	p := NewPool(nil)
	gate, entered := make(chan struct{}), make(chan struct{})
	client := fakeClient("fixture", 22)
	p.dial = func(context.Context, string, int, string) (*Client, error) {
		close(entered)
		<-gate
		return client, nil
	}
	done := make(chan error, 1)
	go func() { _, err := p.Get(context.Background(), "fixture", 22); done <- err }()
	<-entered
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := <-done; err == nil {
		t.Error("late dial returned a client from a closed pool")
	}
	if client.IsConnected() {
		t.Error("late connection leaked")
		client.Close()
	}
	// Reuse must reject before calling the dial function again.
	p.dial = func(context.Context, string, int, string) (*Client, error) {
		t.Error("closed pool dialed")
		return fakeClient("other", 22), nil
	}
	if _, err := p.Get(context.Background(), "other", 22); err == nil {
		t.Error("closed pool accepted work")
	}
}

func TestDisconnectedClientIsNotReusable(t *testing.T) {
	signer, _ := newTestSigner(t)
	cfg := &cryptossh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peer := make(chan *cryptossh.ServerConn, 1)
	go func() {
		socket, e := ln.Accept()
		if e != nil {
			return
		}
		c, _, _, e := cryptossh.NewServerConn(socket, cfg)
		if e == nil {
			peer <- c
		} else {
			socket.Close()
		}
	}()
	client := &Client{host: "127.0.0.1", port: ln.Addr().(*net.TCPAddr).Port, config: &cryptossh.ClientConfig{User: "fixture", HostKeyCallback: cryptossh.InsecureIgnoreHostKey(), Timeout: time.Second}}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	(<-peer).Close()
	deadline := time.Now().Add(time.Second)
	for client.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if client.IsConnected() {
		t.Fatal("closed transport remains reusable")
	}
}

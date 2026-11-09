package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
)

func TestConnectCancelsSilentHandshake(t *testing.T) {
	for _, configured := range []bool{false, true} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		accepted := make(chan net.Conn, 1)
		go func() {
			c, e := ln.Accept()
			if e == nil {
				accepted <- c
			}
		}()
		cfg := &cryptossh.ClientConfig{User: "fixture", HostKeyCallback: cryptossh.InsecureIgnoreHostKey()}
		ctx, cancel := context.WithCancel(context.Background())
		if configured {
			cfg.Timeout = 20 * time.Millisecond
		}
		client := &Client{host: "127.0.0.1", port: ln.Addr().(*net.TCPAddr).Port, config: cfg}
		done := make(chan error, 1)
		go func() { done <- client.Connect(ctx) }()
		peer := <-accepted
		if !configured {
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Error("silent handshake succeeded")
			}
		case <-time.After(time.Second):
			t.Error("handshake outlived cancellation/timeout")
			peer.Close()
			<-done
		}
		cancel()
		peer.Close()
		ln.Close()
		client.Close()
	}
}

type stalledConn struct {
	nopConn
	stage    string
	entered  chan struct{}
	closed   chan struct{}
	requests chan *cryptossh.Request
	once     sync.Once
}

func (c *stalledConn) Close() error {
	c.once.Do(func() { close(c.closed); close(c.requests) })
	return nil
}
func (c *stalledConn) OpenChannel(string, []byte) (cryptossh.Channel, <-chan *cryptossh.Request, error) {
	if c.stage == "open" {
		close(c.entered)
		<-c.closed
		return nil, nil, io.EOF
	}
	return &stalledChannel{conn: c}, c.requests, nil
}

type stalledChannel struct{ conn *stalledConn }

func (c *stalledChannel) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *stalledChannel) Write(p []byte) (int, error) { return len(p), nil }
func (c *stalledChannel) Close() error                { return nil }
func (c *stalledChannel) CloseWrite() error           { return nil }
func (c *stalledChannel) Stderr() io.ReadWriter       { return c }
func (c *stalledChannel) SendRequest(kind string, _ bool, _ []byte) (bool, error) {
	if kind != "exec" {
		return true, nil
	}
	close(c.conn.entered)
	if c.conn.stage == "start" {
		<-c.conn.closed
		return false, io.EOF
	}
	return true, nil
}

func TestExecCancellationAtEveryProtocolStage(t *testing.T) {
	for _, stage := range []string{"open", "start", "wait"} {
		t.Run(stage, func(t *testing.T) {
			transport := &stalledConn{stage: stage, entered: make(chan struct{}), closed: make(chan struct{}), requests: make(chan *cryptossh.Request)}
			client := &Client{conn: &cryptossh.Client{Conn: transport}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := client.Exec(ctx, "fixture"); done <- err }()
			<-transport.entered
			if stage == "wait" {
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("error=%v", err)
				}
			case <-time.After(time.Second):
				t.Error("command outlived cancellation")
				transport.Close()
				<-done
			}
			transport.Close()
		})
	}
}

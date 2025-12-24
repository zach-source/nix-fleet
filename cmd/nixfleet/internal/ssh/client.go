package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client represents an SSH connection to a host
type Client struct {
	host       string
	port       int
	user       string
	conn       *ssh.Client
	mu         sync.Mutex
	lastUsed   time.Time
	config     *ssh.ClientConfig
	knownHosts ssh.HostKeyCallback
}

// ClientConfig holds configuration for SSH clients
type ClientConfig struct {
	User           string
	Port           int
	Timeout        time.Duration
	KeyFiles       []string
	UseAgent       bool
	KnownHostsFile string
	StrictHostKeys bool
}

// DefaultConfig returns a default SSH client configuration
func DefaultConfig() *ClientConfig {
	home, _ := os.UserHomeDir()
	return &ClientConfig{
		User:           "deploy",
		Port:           22,
		Timeout:        30 * time.Second,
		UseAgent:       true,
		KnownHostsFile: filepath.Join(home, ".ssh", "known_hosts"),
		StrictHostKeys: true,
		KeyFiles: []string{
			filepath.Join(home, ".ssh", "id_ed25519"),
			filepath.Join(home, ".ssh", "id_rsa"),
		},
	}
}

// NewClient creates a new SSH client for the given host
func NewClient(host string, cfg *ClientConfig) (*Client, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, fmt.Errorf("building auth methods: %w", err)
	}

	var hostKeyCallback ssh.HostKeyCallback
	if cfg.StrictHostKeys && cfg.KnownHostsFile != "" {
		hostKeyCallback, err = knownhosts.New(cfg.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("loading known_hosts: %w", err)
		}
	} else {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	sshConfig := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         cfg.Timeout,
	}

	return &Client{
		host:       host,
		port:       cfg.Port,
		user:       cfg.User,
		config:     sshConfig,
		knownHosts: hostKeyCallback,
	}, nil
}

func buildAuthMethods(cfg *ClientConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// Try SSH agent first
	if cfg.UseAgent {
		if agentAuth := sshAgentAuth(); agentAuth != nil {
			methods = append(methods, agentAuth)
		}
	}

	// Then try key files
	for _, keyFile := range cfg.KeyFiles {
		if auth, err := publicKeyAuth(keyFile); err == nil {
			methods = append(methods, auth)
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no authentication methods available")
	}

	return methods, nil
}

func sshAgentAuth() ssh.AuthMethod {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}

	agentClient := agent.NewClient(conn)
	return ssh.PublicKeysCallback(agentClient.Signers)
}

func publicKeyAuth(keyFile string) (ssh.AuthMethod, error) {
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}

	return ssh.PublicKeys(signer), nil
}

// Connect establishes the SSH connection
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil // already connected
	}

	addr := fmt.Sprintf("%s:%d", c.host, c.port)

	var d net.Dialer
	netConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, c.config)
	if err != nil {
		netConn.Close()
		return fmt.Errorf("ssh handshake: %w", err)
	}

	c.conn = ssh.NewClient(sshConn, chans, reqs)
	c.lastUsed = time.Now()

	return nil
}

// Close closes the SSH connection
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}

	err := c.conn.Close()
	c.conn = nil
	return err
}

// IsConnected returns true if the client is connected
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// ExecResult holds the result of a command execution
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Exec executes a command on the remote host
func (c *Client) Exec(ctx context.Context, cmd string) (*ExecResult, error) {
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("not connected")
	}
	conn := c.conn
	c.mu.Unlock()

	session, err := conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}
	defer session.Close()

	// Set up pipes
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	// Start command
	if err := session.Start(cmd); err != nil {
		return nil, fmt.Errorf("starting command: %w", err)
	}

	// Read output with context cancellation
	var stdoutBuf, stderrBuf []byte
	var readErr error

	done := make(chan struct{})
	go func() {
		stdoutBuf, _ = io.ReadAll(stdout)
		stderrBuf, _ = io.ReadAll(stderr)
		close(done)
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		return nil, ctx.Err()
	case <-done:
	}

	// Wait for command to finish
	exitCode := 0
	if err := session.Wait(); err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			readErr = err
		}
	}

	if readErr != nil {
		return nil, readErr
	}

	c.mu.Lock()
	c.lastUsed = time.Now()
	c.mu.Unlock()

	return &ExecResult{
		Stdout:   string(stdoutBuf),
		Stderr:   string(stderrBuf),
		ExitCode: exitCode,
	}, nil
}

// ExecSudo executes a command with sudo on the remote host
func (c *Client) ExecSudo(ctx context.Context, cmd string) (*ExecResult, error) {
	return c.Exec(ctx, fmt.Sprintf("sudo %s", cmd))
}

// Host returns the hostname
func (c *Client) Host() string {
	return c.host
}

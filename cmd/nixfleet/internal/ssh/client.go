package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/nixfleet/nixfleet/internal/inventory"
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
			filepath.Join(home, ".ssh", "nixfleet"),
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

	// A pinned key is exclusive: offering the defaults and the agent as well
	// would spend the server's MaxAuthTries budget on keys already known to be
	// wrong, and the host that needs pinning is precisely the host that
	// rejects everything else. This mirrors ssh(1) -o IdentitiesOnly=yes.
	if kf := inventory.HostKey(host); kf != "" {
		pinned := *cfg
		pinned.KeyFiles = []string{kf}
		pinned.UseAgent = false
		cfg = &pinned
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

	// Dedicated key files come first, the agent second.
	//
	// The agent offers every key it holds, and each one the server rejects
	// counts against sshd's MaxAuthTries (6 by default). A workstation with a
	// handful of unrelated keys loaded therefore burns through the budget and
	// the connection is refused before the fleet key is ever offered — which
	// presents as "unable to authenticate ... no supported methods remain",
	// naming nothing useful.
	//
	// This stayed hidden on established hosts whose authorized_keys happened to
	// include an agent key, and only appeared on a freshly bootstrapped host
	// carrying the fleet key alone — i.e. the normal case for every new host.
	for _, keyFile := range cfg.KeyFiles {
		if auth, err := publicKeyAuth(keyFile); err == nil {
			methods = append(methods, auth)
		}
	}

	if cfg.UseAgent {
		if agentAuth := sshAgentAuth(); agentAuth != nil {
			methods = append(methods, agentAuth)
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
	return c.exec(ctx, cmd, nil)
}

// ExecStdin executes a command on the remote host with stdin fed from stdin,
// which is closed once written. Secrets belong here and never in cmd: argv is
// readable by every user on the remote host via /proc.
func (c *Client) ExecStdin(ctx context.Context, cmd string, stdin []byte) (*ExecResult, error) {
	return c.exec(ctx, cmd, stdin)
}

// ExecSudoStdin is ExecStdin under a root shell.
func (c *Client) ExecSudoStdin(ctx context.Context, cmd string, stdin []byte) (*ExecResult, error) {
	return c.exec(ctx, sudoCommand(cmd), stdin)
}

func (c *Client) exec(ctx context.Context, cmd string, stdin []byte) (*ExecResult, error) {
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

	// Session.Stdin is copied (and closed on EOF) by Start in its own
	// goroutine, so a large payload can't deadlock against our output reader.
	if stdin != nil {
		session.Stdin = bytes.NewReader(stdin)
	}

	// Start command
	if err := session.Start(cmd); err != nil {
		return nil, fmt.Errorf("starting command: %w", err)
	}

	// Read output with context cancellation
	outputs := drainOutput(stdout, stderr)

	var out execOutput
	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		return nil, ctx.Err()
	case out = <-outputs:
	}

	// Wait for command to finish
	exitCode := 0
	if err := session.Wait(); err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			return nil, err
		}
	}

	c.mu.Lock()
	c.lastUsed = time.Now()
	c.mu.Unlock()

	return &ExecResult{
		Stdout:   string(out.stdout),
		Stderr:   string(out.stderr),
		ExitCode: exitCode,
	}, nil
}

type execOutput struct {
	stdout []byte
	stderr []byte
}

// drainOutput reads both pipes at the same time and delivers the pair once
// both hit EOF.
//
// Reading them one after the other deadlocks: stdout and stderr are separate
// channels multiplexed over one SSH connection, each with its own flow-control
// window. A command that writes more than a window's worth to stderr (a noisy
// nix build, a compiler) blocks on that write, which blocks the whole remote
// process, so it never closes stdout either -- and the sequential read is
// still waiting on stdout. Neither side moves again.
//
// The channel is buffered so the goroutine never blocks on delivery when the
// caller has already given up on a cancelled context.
func drainOutput(stdout, stderr io.Reader) <-chan execOutput {
	outputs := make(chan execOutput, 1)

	var (
		wg  sync.WaitGroup
		out execOutput
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		out.stdout, _ = io.ReadAll(stdout)
	}()
	go func() {
		defer wg.Done()
		out.stderr, _ = io.ReadAll(stderr)
	}()

	go func() {
		wg.Wait()
		outputs <- out
	}()

	return outputs
}

// ExecSudo executes a command with sudo on the remote host. The whole command
// runs under a root shell, so pipes, redirects and && chains are all privileged.
func (c *Client) ExecSudo(ctx context.Context, cmd string) (*ExecResult, error) {
	return c.Exec(ctx, sudoCommand(cmd))
}

func sudoCommand(cmd string) string {
	return "sudo sh -c " + ShellQuote(cmd)
}

// ShellQuote single-quotes s for safe use as one POSIX shell word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Host returns the hostname
func (c *Client) Host() string {
	return c.host
}

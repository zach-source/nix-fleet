package ssh

import (
	"context"
	"fmt"
	"strings"
)

// MockClient is a mock SSH client for testing
type MockClient struct {
	// Commands maps command strings to their expected results
	Commands map[string]*ExecResult
	// DefaultResult is returned for unregistered commands
	DefaultResult *ExecResult
	// ExecLog records executed commands
	ExecLog []string
	// Closed indicates if Close was called
	Closed bool
	// FailConnect makes connection attempts fail
	FailConnect bool
	// ConnectError is the error to return on connection failure
	ConnectError error
}

// NewMockClient creates a new mock SSH client
func NewMockClient() *MockClient {
	return &MockClient{
		Commands: make(map[string]*ExecResult),
		DefaultResult: &ExecResult{
			Stdout:   "",
			Stderr:   "",
			ExitCode: 0,
		},
		ExecLog: make([]string, 0),
	}
}

// RegisterCommand registers a command and its expected result
func (m *MockClient) RegisterCommand(cmd string, result *ExecResult) {
	m.Commands[cmd] = result
}

// RegisterCommandOutput is a helper to register simple command output
func (m *MockClient) RegisterCommandOutput(cmd, stdout string, exitCode int) {
	m.Commands[cmd] = &ExecResult{
		Stdout:   stdout,
		ExitCode: exitCode,
	}
}

// Exec executes a command (mock implementation)
func (m *MockClient) Exec(ctx context.Context, cmd string) (*ExecResult, error) {
	m.ExecLog = append(m.ExecLog, cmd)

	// Check for exact match first
	if result, ok := m.Commands[cmd]; ok {
		return result, nil
	}

	// Check for prefix matches (for parameterized commands)
	for pattern, result := range m.Commands {
		if strings.HasPrefix(cmd, pattern) {
			return result, nil
		}
	}

	return m.DefaultResult, nil
}

// ExecSudo executes a command with sudo (mock implementation)
func (m *MockClient) ExecSudo(ctx context.Context, cmd string) (*ExecResult, error) {
	return m.Exec(ctx, "sudo "+cmd)
}

// Close closes the mock client
func (m *MockClient) Close() error {
	m.Closed = true
	return nil
}

// CommandExecuted checks if a command was executed
func (m *MockClient) CommandExecuted(cmd string) bool {
	for _, executed := range m.ExecLog {
		if executed == cmd || strings.Contains(executed, cmd) {
			return true
		}
	}
	return false
}

// ClearLog clears the execution log
func (m *MockClient) ClearLog() {
	m.ExecLog = make([]string, 0)
}

// MockPool is a mock connection pool for testing
type MockPool struct {
	// Clients maps host:port to mock clients
	Clients map[string]*MockClient
	// DefaultClient is returned for unknown hosts
	DefaultClient *MockClient
	// FailHosts is a list of hosts that should fail connection
	FailHosts map[string]error
}

// NewMockPool creates a new mock pool
func NewMockPool() *MockPool {
	return &MockPool{
		Clients:       make(map[string]*MockClient),
		DefaultClient: NewMockClient(),
		FailHosts:     make(map[string]error),
	}
}

// RegisterHost registers a mock client for a host
func (p *MockPool) RegisterHost(host string, port int, client *MockClient) {
	key := fmt.Sprintf("%s:%d", host, port)
	p.Clients[key] = client
}

// FailHost makes a host fail connection with an error
func (p *MockPool) FailHost(host string, port int, err error) {
	key := fmt.Sprintf("%s:%d", host, port)
	p.FailHosts[key] = err
}

// Get returns a mock client for a host
func (p *MockPool) Get(ctx context.Context, host string, port int) (*MockClient, error) {
	return p.GetWithUser(ctx, host, port, "")
}

// GetWithUser returns a mock client for a host with a specific user
func (p *MockPool) GetWithUser(ctx context.Context, host string, port int, user string) (*MockClient, error) {
	if port == 0 {
		port = 22
	}
	key := fmt.Sprintf("%s:%d", host, port)

	// Check if host should fail
	if err, ok := p.FailHosts[key]; ok {
		return nil, err
	}

	// Return registered client
	if client, ok := p.Clients[key]; ok {
		return client, nil
	}

	// Return default client
	return p.DefaultClient, nil
}

// Close closes all mock clients
func (p *MockPool) Close() error {
	for _, client := range p.Clients {
		client.Close()
	}
	return nil
}

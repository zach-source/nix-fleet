package ssh

import (
	"context"
	"errors"
	"testing"
)

func TestMockClient(t *testing.T) {
	client := NewMockClient()

	// Test default result
	result, err := client.Exec(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", result.ExitCode)
	}

	// Test registered command
	client.RegisterCommand("hostname", &ExecResult{
		Stdout:   "testhost\n",
		ExitCode: 0,
	})

	result, err = client.Exec(context.Background(), "hostname")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.Stdout != "testhost\n" {
		t.Errorf("Expected 'testhost\\n', got '%s'", result.Stdout)
	}
}

func TestMockClientRegisterCommandOutput(t *testing.T) {
	client := NewMockClient()

	client.RegisterCommandOutput("uname -a", "Linux testhost 5.4.0\n", 0)

	result, err := client.Exec(context.Background(), "uname -a")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.Stdout != "Linux testhost 5.4.0\n" {
		t.Errorf("Unexpected output: %s", result.Stdout)
	}
}

func TestMockClientExecLog(t *testing.T) {
	client := NewMockClient()

	client.Exec(context.Background(), "echo 1")
	client.Exec(context.Background(), "echo 2")
	client.Exec(context.Background(), "echo 3")

	if len(client.ExecLog) != 3 {
		t.Errorf("Expected 3 commands in log, got %d", len(client.ExecLog))
	}

	if !client.CommandExecuted("echo 1") {
		t.Error("Expected 'echo 1' to be executed")
	}
	if client.CommandExecuted("echo 4") {
		t.Error("Did not expect 'echo 4' to be executed")
	}

	client.ClearLog()
	if len(client.ExecLog) != 0 {
		t.Error("Log should be empty after ClearLog")
	}
}

func TestMockClientExecSudo(t *testing.T) {
	client := NewMockClient()
	client.RegisterCommand("sudo apt-get update", &ExecResult{
		Stdout:   "Done\n",
		ExitCode: 0,
	})

	result, err := client.ExecSudo(context.Background(), "apt-get update")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.Stdout != "Done\n" {
		t.Errorf("Unexpected output: %s", result.Stdout)
	}
}

func TestMockClientClose(t *testing.T) {
	client := NewMockClient()

	if client.Closed {
		t.Error("Client should not be closed initially")
	}

	client.Close()

	if !client.Closed {
		t.Error("Client should be closed after Close()")
	}
}

func TestMockPool(t *testing.T) {
	pool := NewMockPool()

	// Create a client for a specific host
	hostClient := NewMockClient()
	hostClient.RegisterCommandOutput("hostname", "myhost\n", 0)
	pool.RegisterHost("10.0.0.1", 22, hostClient)

	// Get the registered client
	client, err := pool.Get(context.Background(), "10.0.0.1", 22)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, _ := client.Exec(context.Background(), "hostname")
	if result.Stdout != "myhost\n" {
		t.Errorf("Expected 'myhost\\n', got '%s'", result.Stdout)
	}

	// Get default client for unknown host
	client, err = pool.Get(context.Background(), "10.0.0.2", 22)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	// Should use default client
	if client != pool.DefaultClient {
		t.Error("Expected default client for unknown host")
	}
}

func TestMockPoolFailHost(t *testing.T) {
	pool := NewMockPool()

	expectedErr := errors.New("connection refused")
	pool.FailHost("10.0.0.1", 22, expectedErr)

	_, err := pool.Get(context.Background(), "10.0.0.1", 22)
	if err != expectedErr {
		t.Errorf("Expected error '%v', got '%v'", expectedErr, err)
	}
}

func TestMockPoolDefaultPort(t *testing.T) {
	pool := NewMockPool()

	// Register with port 22
	hostClient := NewMockClient()
	pool.RegisterHost("10.0.0.1", 22, hostClient)

	// Get with port 0 should default to 22
	client, err := pool.Get(context.Background(), "10.0.0.1", 0)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if client != hostClient {
		t.Error("Expected registered client when using default port")
	}
}

func TestMockPoolClose(t *testing.T) {
	pool := NewMockPool()

	client1 := NewMockClient()
	client2 := NewMockClient()
	pool.RegisterHost("10.0.0.1", 22, client1)
	pool.RegisterHost("10.0.0.2", 22, client2)

	pool.Close()

	if !client1.Closed {
		t.Error("Client1 should be closed")
	}
	if !client2.Closed {
		t.Error("Client2 should be closed")
	}
}

func TestNewPool(t *testing.T) {
	pool := NewPool(nil)
	if pool == nil {
		t.Error("NewPool should not return nil")
	}
	pool.Close()
}

func TestPoolRemove(t *testing.T) {
	pool := NewPool(nil)

	// Remove should not panic on non-existent host
	pool.Remove("10.0.0.1", 22)

	pool.Close()
}

func TestNewExecutor(t *testing.T) {
	pool := NewPool(nil)
	executor := NewExecutor(pool, 5)

	if executor == nil {
		t.Error("NewExecutor should not return nil")
	}
	if executor.maxParallel != 5 {
		t.Errorf("Expected maxParallel 5, got %d", executor.maxParallel)
	}

	pool.Close()
}

func TestFilterFailed(t *testing.T) {
	results := []HostResult{
		{Host: nil, Error: nil, Success: true},
		{Host: nil, Error: errors.New("error1"), Success: false},
		{Host: nil, Error: nil, Success: true},
		{Host: nil, Error: errors.New("error2"), Success: false},
	}

	failed := FilterFailed(results)
	if len(failed) != 2 {
		t.Errorf("Expected 2 failed results, got %d", len(failed))
	}
}

func TestCountSuccess(t *testing.T) {
	results := []HostResult{
		{Host: nil, Error: nil, Success: true},
		{Host: nil, Error: errors.New("error"), Success: false},
		{Host: nil, Error: nil, Success: true},
	}

	if CountSuccess(results) != 2 {
		t.Errorf("Expected 2 successes, got %d", CountSuccess(results))
	}
}

func TestCountErrors(t *testing.T) {
	results := []HostResult{
		{Host: nil, Error: nil, Success: true},
		{Host: nil, Error: errors.New("error1"), Success: false},
		{Host: nil, Error: errors.New("error2"), Success: false},
	}

	if CountErrors(results) != 2 {
		t.Errorf("Expected 2 errors, got %d", CountErrors(results))
	}
}

func TestExecResult(t *testing.T) {
	result := &ExecResult{
		Stdout:   "output\n",
		Stderr:   "error\n",
		ExitCode: 1,
	}

	if result.Stdout != "output\n" {
		t.Errorf("Unexpected stdout: %s", result.Stdout)
	}
	if result.Stderr != "error\n" {
		t.Errorf("Unexpected stderr: %s", result.Stderr)
	}
	if result.ExitCode != 1 {
		t.Errorf("Unexpected exit code: %d", result.ExitCode)
	}
}

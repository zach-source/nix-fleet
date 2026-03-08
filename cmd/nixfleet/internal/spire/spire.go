package spire

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	TrustDomain       = "nixfleet.stigen.ai"
	DefaultServerAddr = ""
	DefaultServerPort = ""
	SpireSubDir       = ".nixfleet/spire"
	AgentSocket       = "/tmp/spire-agent/public/api.sock"
	LaunchdLabel      = "ai.stigen.nixfleet.spire-agent"
	LaunchdPlistName  = LaunchdLabel + ".plist"
)

// ClientConfig holds connection info for the SPIRE server, read from the cluster.
type ClientConfig struct {
	ServerAddress string
	ServerPort    string
	TrustDomain   string
}

// FetchClientConfig reads SPIRE client connection info from the spire-client-config ConfigMap.
func FetchClientConfig(ctx context.Context) (*ClientConfig, error) {
	cmd := exec.CommandContext(ctx,
		"kubectl", "get", "configmap", "spire-client-config",
		"-n", "spire-system",
		"-o", "jsonpath={.data.server-address},{.data.server-port},{.data.trust-domain}",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("fetch spire-client-config: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	parts := strings.Split(strings.TrimSpace(stdout.String()), ",")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("spire-client-config ConfigMap missing required fields (server-address, server-port, trust-domain)")
	}

	return &ClientConfig{
		ServerAddress: parts[0],
		ServerPort:    parts[1],
		TrustDomain:   parts[2],
	}, nil
}

// SpireDir returns the SPIRE config directory under the user's home.
func SpireDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(home, SpireSubDir), nil
}

// CheckBinary verifies spire-agent is on PATH.
func CheckBinary() (string, error) {
	path, err := exec.LookPath("spire-agent")
	if err != nil {
		return "", fmt.Errorf("spire-agent not found on PATH — install with: brew install spiffe/spire/spire")
	}
	return path, nil
}

// GenerateJoinToken creates a one-time join token via the SPIRE server.
func GenerateJoinToken(ctx context.Context, hostname string) (string, error) {
	spiffeID := fmt.Sprintf("spiffe://%s/host/%s", TrustDomain, hostname)

	cmd := exec.CommandContext(ctx,
		"kubectl", "exec", "-n", "spire-system", "spire-server-0",
		"-c", "spire-server", "--",
		"/opt/spire/bin/spire-server", "token", "generate",
		"-spiffeID", spiffeID,
		"-ttl", "600",
		"-socketPath", "/tmp/spire-server/private/api.sock",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("generate join token: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	// Output format: "Token: <token>\n"
	output := strings.TrimSpace(stdout.String())
	if strings.HasPrefix(output, "Token: ") {
		return strings.TrimPrefix(output, "Token: "), nil
	}
	return output, nil
}

// FetchTrustBundle retrieves the SPIRE trust bundle in PEM format.
func FetchTrustBundle(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx,
		"kubectl", "exec", "-n", "spire-system", "spire-server-0",
		"-c", "spire-server", "--",
		"/opt/spire/bin/spire-server", "bundle", "show",
		"-format", "pem",
		"-socketPath", "/tmp/spire-server/private/api.sock",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("fetch trust bundle: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	bundle := strings.TrimSpace(stdout.String())
	if !strings.Contains(bundle, "BEGIN CERTIFICATE") {
		return "", fmt.Errorf("trust bundle does not contain PEM certificates")
	}
	return bundle, nil
}

// WriteAgentConfig writes the SPIRE agent config and trust bundle to disk.
func WriteAgentConfig(joinToken, trustBundle, serverHost, serverPort string) (configPath string, err error) {
	dir, err := SpireDir()
	if err != nil {
		return "", err
	}

	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}

	// Write trust bundle
	bundlePath := filepath.Join(dir, "trust-bundle.pem")
	if err := os.WriteFile(bundlePath, []byte(trustBundle+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write trust bundle: %w", err)
	}

	// Write agent config
	configPath = filepath.Join(dir, "agent.conf")
	cfg := AgentConfig{
		DataDir:         dataDir,
		ServerHost:      serverHost,
		ServerPort:      serverPort,
		SocketPath:      AgentSocket,
		TrustBundlePath: bundlePath,
		TrustDomain:     TrustDomain,
		JoinToken:       joinToken,
	}

	var buf bytes.Buffer
	if err := agentConfTmpl.Execute(&buf, cfg); err != nil {
		return "", fmt.Errorf("render agent config: %w", err)
	}
	if err := os.WriteFile(configPath, buf.Bytes(), 0600); err != nil {
		return "", fmt.Errorf("write agent config: %w", err)
	}

	return configPath, nil
}

// CreateRegistrationEntry registers a workload entry for the host so local processes can get SVIDs.
func CreateRegistrationEntry(ctx context.Context, hostname string) error {
	parentID := fmt.Sprintf("spiffe://%s/host/%s", TrustDomain, hostname)
	spiffeID := fmt.Sprintf("spiffe://%s/host/%s/workload", TrustDomain, hostname)
	uid := strconv.Itoa(os.Getuid())

	cmd := exec.CommandContext(ctx,
		"kubectl", "exec", "-n", "spire-system", "spire-server-0",
		"-c", "spire-server", "--",
		"/opt/spire/bin/spire-server", "entry", "create",
		"-parentID", parentID,
		"-spiffeID", spiffeID,
		"-selector", "unix:uid:"+uid,
		"-socketPath", "/tmp/spire-server/private/api.sock",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
		// Entry may already exist
		if strings.Contains(combined, "similar entry already exists") {
			return nil
		}
		return fmt.Errorf("create registration entry: %s: %w", combined, err)
	}
	return nil
}

// StartPortForward starts kubectl port-forward to the SPIRE server and returns
// a cleanup function. The local port is returned.
func StartPortForward(ctx context.Context, localPort string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx,
		"kubectl", "port-forward", "-n", "spire-system",
		"statefulset/spire-server", localPort+":8081",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start port-forward: %w", err)
	}

	// Wait for the port to be available
	for i := 0; i < 20; i++ {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+localPort, time.Second)
		if err == nil {
			conn.Close()
			return cmd, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	return nil, fmt.Errorf("port-forward did not become available within 10s")
}

// BootstrapAgent runs the SPIRE agent once with a join token to complete initial
// attestation. After attestation succeeds, the agent's SVID is saved to disk
// and the agent can restart without a token.
func BootstrapAgent(ctx context.Context, agentBinary, configPath, joinToken string) error {
	// Ensure socket directory exists
	if err := os.MkdirAll(filepath.Dir(AgentSocket), 0755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}

	dir, _ := SpireDir()
	logFile := filepath.Join(dir, "spire-agent-bootstrap.log")

	// Start agent with join token for initial attestation
	cmd := exec.CommandContext(ctx, agentBinary, "run",
		"-config", configPath,
		"-joinToken", joinToken,
	)

	f, err := os.Create(logFile)
	if err != nil {
		return fmt.Errorf("create bootstrap log: %w", err)
	}
	cmd.Stdout = f
	cmd.Stderr = f

	if err := cmd.Start(); err != nil {
		f.Close()
		return fmt.Errorf("start agent: %w", err)
	}

	// Poll the log file for attestation success or failure
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			cmd.Process.Signal(syscall.SIGTERM)
			f.Close()
			return ctx.Err()
		default:
		}

		logContent, _ := os.ReadFile(logFile)
		logStr := string(logContent)

		if strings.Contains(logStr, "Node attestation was successful") {
			// Attestation succeeded — SVID saved to disk, stop the bootstrap agent
			cmd.Process.Signal(syscall.SIGTERM)
			cmd.Wait()
			f.Close()
			return nil
		}

		if strings.Contains(logStr, "Agent crashed") {
			cmd.Process.Signal(syscall.SIGTERM)
			cmd.Wait()
			f.Close()
			return fmt.Errorf("agent crashed during bootstrap — check %s:\n%s", logFile, lastLines(logStr, 5))
		}

		time.Sleep(time.Second)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	f.Close()
	logContent, _ := os.ReadFile(logFile)
	return fmt.Errorf("attestation did not complete within 30s — check %s:\n%s", logFile, lastLines(string(logContent), 5))
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// InstallLaunchd writes and loads a launchd plist to run the SPIRE agent
// (without a join token — the agent already attested during bootstrap).
func InstallLaunchd(agentBinary, configPath string) error {
	dir, err := SpireDir()
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", LaunchdPlistName)

	cfg := LaunchdConfig{
		AgentBinary: agentBinary,
		ConfigPath:  configPath,
		LogDir:      dir,
	}

	var buf bytes.Buffer
	if err := launchdPlistTmpl.Execute(&buf, cfg); err != nil {
		return fmt.Errorf("render launchd plist: %w", err)
	}
	if err := os.WriteFile(plistPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("write launchd plist: %w", err)
	}

	// Load the service
	cmd := exec.Command("launchctl", "load", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// AgentStatus holds the state of the local SPIRE agent.
type AgentStatus struct {
	Running    bool
	PID        int
	SocketPath string
	CanFetchID bool
	SVID       string
}

// GetStatus checks the local SPIRE agent state.
func GetStatus() (*AgentStatus, error) {
	status := &AgentStatus{
		SocketPath: AgentSocket,
	}

	// Check launchd
	cmd := exec.Command("launchctl", "list", LaunchdLabel)
	out, err := cmd.CombinedOutput()
	if err == nil {
		status.Running = true
		// Parse PID from launchctl output
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "\"PID\" = ") {
				pidStr := strings.TrimSuffix(strings.TrimPrefix(line, "\"PID\" = "), ";")
				if pid, err := strconv.Atoi(pidStr); err == nil {
					status.PID = pid
				}
			}
		}
	}

	// Check socket exists
	if _, err := os.Stat(AgentSocket); err != nil {
		return status, nil
	}

	// Try to fetch an SVID
	agentBin, err := exec.LookPath("spire-agent")
	if err != nil {
		return status, nil
	}

	fetchCmd := exec.Command(agentBin, "api", "fetch", "-socketPath", AgentSocket, "-silent")
	fetchOut, err := fetchCmd.CombinedOutput()
	if err == nil {
		status.CanFetchID = true
		for _, line := range strings.Split(string(fetchOut), "\n") {
			if strings.HasPrefix(line, "SPIFFE ID:") {
				status.SVID = strings.TrimSpace(strings.TrimPrefix(line, "SPIFFE ID:"))
				break
			}
		}
	}

	return status, nil
}

// StopAgent stops the SPIRE agent and optionally cleans up config.
func StopAgent(keepConfig bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home directory: %w", err)
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", LaunchdPlistName)

	// Unload from launchd
	if _, err := os.Stat(plistPath); err == nil {
		cmd := exec.Command("launchctl", "unload", plistPath)
		cmd.Run() // best-effort
		os.Remove(plistPath)
	}

	// Also try killing by PID if launchd didn't work
	cmd := exec.Command("launchctl", "list", LaunchdLabel)
	if out, err := cmd.CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "\"PID\" = ") {
				pidStr := strings.TrimSuffix(strings.TrimPrefix(line, "\"PID\" = "), ";")
				if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
					syscall.Kill(pid, syscall.SIGTERM)
				}
			}
		}
	}

	// Remove socket
	os.Remove(AgentSocket)

	if !keepConfig {
		dir, err := SpireDir()
		if err == nil {
			os.RemoveAll(dir)
		}
	}

	return nil
}

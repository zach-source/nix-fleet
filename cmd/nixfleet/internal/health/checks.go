// Package health implements post-deployment health checks
package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// CheckType represents the type of health check
type CheckType string

const (
	CheckTypeSystemd CheckType = "systemd"
	CheckTypeLaunchd CheckType = "launchd"
	CheckTypeHTTP    CheckType = "http"
	CheckTypeTCP     CheckType = "tcp"
	CheckTypeCommand CheckType = "command"
)

// HealthCheckConfig defines a health check configuration
type HealthCheckConfig struct {
	Name           string        `json:"name" yaml:"name"`
	Type           CheckType     `json:"type" yaml:"type"`
	Target         string        `json:"target" yaml:"target"` // unit name, URL, host:port, or command
	ExpectedStatus int           `json:"expectedStatus,omitempty" yaml:"expectedStatus,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Retries        int           `json:"retries,omitempty" yaml:"retries,omitempty"`
	RetryDelay     time.Duration `json:"retryDelay,omitempty" yaml:"retryDelay,omitempty"`
}

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	Name    string    `json:"name"`
	Type    CheckType `json:"type"`
	Passed  bool      `json:"passed"`
	Message string    `json:"message"`
	Details string    `json:"details,omitempty"`
	Latency string    `json:"latency,omitempty"`
}

// HealthResults contains all health check results for a host
type HealthResults struct {
	Host    string              `json:"host"`
	Passed  bool                `json:"passed"`
	Checks  []HealthCheckResult `json:"checks"`
	Summary string              `json:"summary"`
}

// FailurePolicy defines what to do when health checks fail
type FailurePolicy string

const (
	PolicyRollback FailurePolicy = "rollback"
	PolicyHalt     FailurePolicy = "halt"
	PolicyContinue FailurePolicy = "continue"
)

// Checker performs health checks on remote hosts
type Checker struct{}

// NewChecker creates a new health checker
func NewChecker() *Checker {
	return &Checker{}
}

// RunChecks executes health checks based on configuration
func (c *Checker) RunChecks(ctx context.Context, client *ssh.Client, configs []HealthCheckConfig) (*HealthResults, error) {
	results := &HealthResults{
		Host:   client.Host(),
		Passed: true,
		Checks: make([]HealthCheckResult, 0),
	}

	for _, config := range configs {
		// Apply defaults
		if config.Timeout == 0 {
			config.Timeout = 10 * time.Second
		}
		if config.Retries == 0 {
			config.Retries = 1
		}
		if config.RetryDelay == 0 {
			config.RetryDelay = 2 * time.Second
		}

		var result HealthCheckResult

		// Retry loop
		for attempt := 0; attempt < config.Retries; attempt++ {
			if attempt > 0 {
				time.Sleep(config.RetryDelay)
			}

			switch config.Type {
			case CheckTypeSystemd:
				result = c.checkSystemdUnit(ctx, client, config)
			case CheckTypeLaunchd:
				result = c.checkLaunchdService(ctx, client, config)
			case CheckTypeHTTP:
				result = c.checkHTTP(ctx, client, config)
			case CheckTypeTCP:
				result = c.checkTCP(ctx, client, config)
			case CheckTypeCommand:
				result = c.checkCommand(ctx, client, config)
			default:
				result = HealthCheckResult{
					Name:    config.Name,
					Type:    config.Type,
					Passed:  false,
					Message: "Unknown check type",
				}
			}

			if result.Passed {
				break
			}
		}

		if !result.Passed && config.Retries > 1 {
			result.Details = fmt.Sprintf("Failed after %d attempts. %s", config.Retries, result.Details)
		}

		results.Checks = append(results.Checks, result)
		if !result.Passed {
			results.Passed = false
		}
	}

	// Generate summary
	passed := 0
	failed := 0
	for _, check := range results.Checks {
		if check.Passed {
			passed++
		} else {
			failed++
		}
	}
	results.Summary = fmt.Sprintf("%d/%d health checks passed", passed, passed+failed)

	return results, nil
}

// checkSystemdUnit checks the status of a systemd unit
func (c *Checker) checkSystemdUnit(ctx context.Context, client *ssh.Client, config HealthCheckConfig) HealthCheckResult {
	result := HealthCheckResult{
		Name: config.Name,
		Type: CheckTypeSystemd,
	}

	start := time.Now()

	// Check unit status
	cmd := fmt.Sprintf("systemctl is-active %s", config.Target)
	output, err := client.Exec(ctx, cmd)

	result.Latency = time.Since(start).String()

	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("Failed to check unit %s", config.Target)
		result.Details = err.Error()
		return result
	}

	status := strings.TrimSpace(output.Stdout)
	if status != "active" {
		result.Passed = false
		result.Message = fmt.Sprintf("Unit %s is not active", config.Target)
		result.Details = fmt.Sprintf("Status: %s", status)

		// Get more details about the failure
		detailCmd := fmt.Sprintf("systemctl status %s --no-pager -l 2>&1 | head -20", config.Target)
		detailOutput, _ := client.Exec(ctx, detailCmd)
		if detailOutput != nil && detailOutput.Stdout != "" {
			result.Details = detailOutput.Stdout
		}
		return result
	}

	result.Passed = true
	result.Message = fmt.Sprintf("Unit %s is active", config.Target)
	return result
}

// checkLaunchdService checks the status of a launchd service (Darwin)
func (c *Checker) checkLaunchdService(ctx context.Context, client *ssh.Client, config HealthCheckConfig) HealthCheckResult {
	result := HealthCheckResult{
		Name: config.Name,
		Type: CheckTypeLaunchd,
	}

	start := time.Now()

	// Check service status via launchctl
	// launchctl list <label> returns info if service exists
	cmd := fmt.Sprintf("launchctl list %s 2>/dev/null", config.Target)
	output, err := client.Exec(ctx, cmd)

	result.Latency = time.Since(start).String()

	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("Failed to check service %s", config.Target)
		result.Details = err.Error()
		return result
	}

	if output.ExitCode != 0 {
		result.Passed = false
		result.Message = fmt.Sprintf("Service %s not found", config.Target)
		result.Details = "Service is not loaded in launchd"
		return result
	}

	// Parse launchctl output to check if service is running
	// Format: PID	Status	Label
	// Running: "12345	0	com.example.service"
	// Not running: "-	0	com.example.service"
	lines := strings.Split(strings.TrimSpace(output.Stdout), "\n")
	if len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) >= 1 && fields[0] == "-" {
			result.Passed = false
			result.Message = fmt.Sprintf("Service %s is not running", config.Target)
			result.Details = "Service is loaded but has no active PID"

			// Try to get more details
			detailCmd := fmt.Sprintf("launchctl print system/%s 2>&1 | head -20", config.Target)
			detailOutput, _ := client.Exec(ctx, detailCmd)
			if detailOutput != nil && detailOutput.Stdout != "" {
				result.Details = detailOutput.Stdout
			}
			return result
		}
	}

	result.Passed = true
	result.Message = fmt.Sprintf("Service %s is running", config.Target)
	return result
}

// checkHTTP performs an HTTP health check
func (c *Checker) checkHTTP(ctx context.Context, client *ssh.Client, config HealthCheckConfig) HealthCheckResult {
	result := HealthCheckResult{
		Name: config.Name,
		Type: CheckTypeHTTP,
	}

	expectedStatus := config.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = 200
	}

	start := time.Now()

	// Use curl on the remote host to check HTTP endpoint
	cmd := fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code}' --max-time %d '%s'",
		int(config.Timeout.Seconds()), config.Target)
	output, err := client.Exec(ctx, cmd)

	result.Latency = time.Since(start).String()

	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("HTTP check failed for %s", config.Target)
		result.Details = err.Error()
		return result
	}

	statusCode := strings.TrimSpace(output.Stdout)
	if statusCode == "" || output.ExitCode != 0 {
		result.Passed = false
		result.Message = fmt.Sprintf("HTTP request to %s failed", config.Target)
		result.Details = fmt.Sprintf("curl exit code: %d, stderr: %s", output.ExitCode, output.Stderr)
		return result
	}

	if statusCode != fmt.Sprintf("%d", expectedStatus) {
		result.Passed = false
		result.Message = fmt.Sprintf("HTTP %s returned unexpected status", config.Target)
		result.Details = fmt.Sprintf("Expected: %d, Got: %s", expectedStatus, statusCode)
		return result
	}

	result.Passed = true
	result.Message = fmt.Sprintf("HTTP %s returned %s", config.Target, statusCode)
	return result
}

// checkTCP performs a TCP connectivity check
func (c *Checker) checkTCP(ctx context.Context, client *ssh.Client, config HealthCheckConfig) HealthCheckResult {
	result := HealthCheckResult{
		Name: config.Name,
		Type: CheckTypeTCP,
	}

	start := time.Now()

	// Parse host:port from target
	target := config.Target

	// Use bash /dev/tcp for TCP check
	cmd := fmt.Sprintf("timeout %d bash -c 'cat < /dev/null > /dev/tcp/%s' 2>&1 && echo 'ok' || echo 'failed'",
		int(config.Timeout.Seconds()), strings.Replace(target, ":", "/", 1))
	output, err := client.Exec(ctx, cmd)

	result.Latency = time.Since(start).String()

	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("TCP check failed for %s", target)
		result.Details = err.Error()
		return result
	}

	if !strings.Contains(output.Stdout, "ok") {
		result.Passed = false
		result.Message = fmt.Sprintf("TCP connection to %s failed", target)
		result.Details = output.Stdout
		return result
	}

	result.Passed = true
	result.Message = fmt.Sprintf("TCP connection to %s succeeded", target)
	return result
}

// checkCommand runs a custom health check command
func (c *Checker) checkCommand(ctx context.Context, client *ssh.Client, config HealthCheckConfig) HealthCheckResult {
	result := HealthCheckResult{
		Name: config.Name,
		Type: CheckTypeCommand,
	}

	start := time.Now()

	// Run the command with timeout
	cmd := fmt.Sprintf("timeout %d %s", int(config.Timeout.Seconds()), config.Target)
	output, err := client.Exec(ctx, cmd)

	result.Latency = time.Since(start).String()

	if err != nil {
		result.Passed = false
		result.Message = "Command execution failed"
		result.Details = err.Error()
		return result
	}

	if output.ExitCode != 0 {
		result.Passed = false
		result.Message = fmt.Sprintf("Command exited with code %d", output.ExitCode)
		result.Details = output.Stderr
		if output.Stdout != "" {
			result.Details = output.Stdout + "\n" + output.Stderr
		}
		return result
	}

	result.Passed = true
	result.Message = "Command succeeded"
	if output.Stdout != "" {
		result.Details = strings.TrimSpace(output.Stdout)
	}
	return result
}

// CheckSystemdUnits checks multiple systemd units
func (c *Checker) CheckSystemdUnits(ctx context.Context, client *ssh.Client, units []string) []HealthCheckResult {
	results := make([]HealthCheckResult, 0, len(units))
	for _, unit := range units {
		config := HealthCheckConfig{
			Name:    unit,
			Type:    CheckTypeSystemd,
			Target:  unit,
			Timeout: 5 * time.Second,
		}
		results = append(results, c.checkSystemdUnit(ctx, client, config))
	}
	return results
}

// CheckLaunchdServices checks multiple launchd services (Darwin)
func (c *Checker) CheckLaunchdServices(ctx context.Context, client *ssh.Client, services []string) []HealthCheckResult {
	results := make([]HealthCheckResult, 0, len(services))
	for _, service := range services {
		config := HealthCheckConfig{
			Name:    service,
			Type:    CheckTypeLaunchd,
			Target:  service,
			Timeout: 5 * time.Second,
		}
		results = append(results, c.checkLaunchdService(ctx, client, config))
	}
	return results
}

// CheckHTTPEndpoint performs a single HTTP check
func (c *Checker) CheckHTTPEndpoint(ctx context.Context, client *ssh.Client, name, url string, expectedStatus int) HealthCheckResult {
	config := HealthCheckConfig{
		Name:           name,
		Type:           CheckTypeHTTP,
		Target:         url,
		ExpectedStatus: expectedStatus,
		Timeout:        10 * time.Second,
	}
	return c.checkHTTP(ctx, client, config)
}

// CheckTCPPort performs a single TCP check
func (c *Checker) CheckTCPPort(ctx context.Context, client *ssh.Client, name, hostPort string) HealthCheckResult {
	config := HealthCheckConfig{
		Name:    name,
		Type:    CheckTypeTCP,
		Target:  hostPort,
		Timeout: 5 * time.Second,
	}
	return c.checkTCP(ctx, client, config)
}

// CheckCustomCommand runs a custom health check command
func (c *Checker) CheckCustomCommand(ctx context.Context, client *ssh.Client, name, command string) HealthCheckResult {
	config := HealthCheckConfig{
		Name:    name,
		Type:    CheckTypeCommand,
		Target:  command,
		Timeout: 30 * time.Second,
	}
	return c.checkCommand(ctx, client, config)
}

// ConvertFromNixFleetConfig converts nixfleet.healthChecks config to HealthCheckConfig
func ConvertFromNixFleetConfig(name string, cfg map[string]interface{}) HealthCheckConfig {
	config := HealthCheckConfig{
		Name: name,
	}

	if t, ok := cfg["type"].(string); ok {
		config.Type = CheckType(t)
	}

	switch config.Type {
	case CheckTypeSystemd:
		if unit, ok := cfg["unit"].(string); ok {
			config.Target = unit
		}
	case CheckTypeLaunchd:
		if service, ok := cfg["service"].(string); ok {
			config.Target = service
		}
	case CheckTypeHTTP:
		if url, ok := cfg["url"].(string); ok {
			config.Target = url
		}
		if status, ok := cfg["expectedStatus"].(int); ok {
			config.ExpectedStatus = status
		}
	case CheckTypeTCP:
		host := "localhost"
		port := 0
		if h, ok := cfg["host"].(string); ok {
			host = h
		}
		if p, ok := cfg["port"].(int); ok {
			port = p
		}
		config.Target = fmt.Sprintf("%s:%d", host, port)
	case CheckTypeCommand:
		if cmd, ok := cfg["command"].(string); ok {
			config.Target = cmd
		}
	}

	if timeout, ok := cfg["timeout"].(int); ok {
		config.Timeout = time.Duration(timeout) * time.Second
	}

	return config
}

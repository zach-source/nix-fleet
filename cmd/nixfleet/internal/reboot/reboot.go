// Package reboot implements reboot orchestration for managed hosts
package reboot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// RebootStatus represents the reboot requirement status of a host
type RebootStatus struct {
	Required        bool
	Reason          string
	TriggerPackages []string
}

// RebootWindow represents a time window for reboots
type RebootWindow struct {
	DayOfWeek time.Weekday // 0 = Sunday
	StartHour int          // 0-23
	StartMin  int          // 0-59
	EndHour   int          // 0-23
	EndMin    int          // 0-59
}

// RebootConfig holds reboot orchestration configuration
type RebootConfig struct {
	AllowReboot          bool
	Window               *RebootWindow
	MaxConcurrentReboots int
	PreRebootHook        string
	PostRebootHook       string
	WaitTimeout          time.Duration
	WaitInterval         time.Duration
}

// DefaultRebootConfig returns sensible defaults
func DefaultRebootConfig() RebootConfig {
	return RebootConfig{
		AllowReboot:          false,
		Window:               nil, // No window restriction
		MaxConcurrentReboots: 1,
		PreRebootHook:        "",
		PostRebootHook:       "",
		WaitTimeout:          10 * time.Minute,
		WaitInterval:         10 * time.Second,
	}
}

// Orchestrator handles reboot orchestration
type Orchestrator struct {
	config RebootConfig
}

// NewOrchestrator creates a new reboot orchestrator
func NewOrchestrator(config RebootConfig) *Orchestrator {
	return &Orchestrator{config: config}
}

// CheckRebootRequired checks if a host needs a reboot
func (o *Orchestrator) CheckRebootRequired(ctx context.Context, client *ssh.Client, base string) (*RebootStatus, error) {
	switch base {
	case "ubuntu":
		return o.checkUbuntuReboot(ctx, client)
	case "nixos":
		return o.checkNixOSReboot(ctx, client)
	case "darwin":
		return o.checkDarwinReboot(ctx, client)
	default:
		return &RebootStatus{Required: false, Reason: "unsupported base"}, nil
	}
}

// checkUbuntuReboot checks for reboot requirement on Ubuntu
func (o *Orchestrator) checkUbuntuReboot(ctx context.Context, client *ssh.Client) (*RebootStatus, error) {
	status := &RebootStatus{}

	// Check /var/run/reboot-required
	result, err := client.Exec(ctx, "test -f /var/run/reboot-required && echo 'yes' || echo 'no'")
	if err != nil {
		return nil, fmt.Errorf("checking reboot-required: %w", err)
	}

	if strings.TrimSpace(result.Stdout) != "yes" {
		return status, nil
	}

	status.Required = true
	status.Reason = "reboot-required file present"

	// Get triggering packages
	result, err = client.Exec(ctx, "cat /var/run/reboot-required.pkgs 2>/dev/null || true")
	if err == nil && result.Stdout != "" {
		status.TriggerPackages = strings.Split(strings.TrimSpace(result.Stdout), "\n")
	}

	return status, nil
}

// checkNixOSReboot checks for reboot requirement on NixOS
func (o *Orchestrator) checkNixOSReboot(ctx context.Context, client *ssh.Client) (*RebootStatus, error) {
	status := &RebootStatus{}

	// Compare running kernel with booted kernel
	result, err := client.Exec(ctx, `
		RUNNING=$(readlink /run/current-system)
		BOOTED=$(readlink /run/booted-system 2>/dev/null || echo "")
		if [ -z "$BOOTED" ] || [ "$RUNNING" != "$BOOTED" ]; then
			echo "changed"
		else
			echo "same"
		fi
	`)
	if err != nil {
		return nil, fmt.Errorf("checking NixOS system change: %w", err)
	}

	if strings.TrimSpace(result.Stdout) == "changed" {
		status.Required = true
		status.Reason = "system configuration changed since boot"
	}

	// Also check if kernel version changed
	result, err = client.Exec(ctx, `
		RUNNING_KERNEL=$(uname -r)
		CURRENT_KERNEL=$(readlink /run/current-system/kernel 2>/dev/null | xargs -I{} readlink -f {} | xargs basename 2>/dev/null || echo "")
		if [ -n "$CURRENT_KERNEL" ] && [ "$RUNNING_KERNEL" != "$CURRENT_KERNEL" ]; then
			echo "kernel:$CURRENT_KERNEL"
		fi
	`)
	if err == nil && strings.HasPrefix(strings.TrimSpace(result.Stdout), "kernel:") {
		status.Required = true
		newKernel := strings.TrimPrefix(strings.TrimSpace(result.Stdout), "kernel:")
		status.Reason = fmt.Sprintf("new kernel available: %s", newKernel)
	}

	return status, nil
}

// checkDarwinReboot checks for reboot requirement on macOS
func (o *Orchestrator) checkDarwinReboot(ctx context.Context, client *ssh.Client) (*RebootStatus, error) {
	status := &RebootStatus{}

	// Check for pending software updates that require restart
	result, err := client.Exec(ctx, "softwareupdate --list 2>&1 | grep -i restart || true")
	if err != nil {
		return nil, fmt.Errorf("checking macOS updates: %w", err)
	}

	if strings.Contains(result.Stdout, "restart") {
		status.Required = true
		status.Reason = "pending macOS updates require restart"
	}

	return status, nil
}

// ParseRebootWindow parses a window string like "Sun 02:00-04:00"
func ParseRebootWindow(s string) (*RebootWindow, error) {
	if s == "" {
		return nil, nil
	}

	// Pattern: "Day HH:MM-HH:MM" or "HH:MM-HH:MM" (daily)
	dayPattern := regexp.MustCompile(`^(?:(Sun|Mon|Tue|Wed|Thu|Fri|Sat)\s+)?(\d{1,2}):(\d{2})-(\d{1,2}):(\d{2})$`)
	matches := dayPattern.FindStringSubmatch(s)
	if matches == nil {
		return nil, fmt.Errorf("invalid reboot window format: %s (expected 'Day HH:MM-HH:MM' or 'HH:MM-HH:MM')", s)
	}

	window := &RebootWindow{}

	// Parse day of week
	if matches[1] != "" {
		switch matches[1] {
		case "Sun":
			window.DayOfWeek = time.Sunday
		case "Mon":
			window.DayOfWeek = time.Monday
		case "Tue":
			window.DayOfWeek = time.Tuesday
		case "Wed":
			window.DayOfWeek = time.Wednesday
		case "Thu":
			window.DayOfWeek = time.Thursday
		case "Fri":
			window.DayOfWeek = time.Friday
		case "Sat":
			window.DayOfWeek = time.Saturday
		}
	} else {
		window.DayOfWeek = -1 // Any day
	}

	// Parse times
	var err error
	window.StartHour, err = strconv.Atoi(matches[2])
	if err != nil || window.StartHour > 23 {
		return nil, fmt.Errorf("invalid start hour: %s", matches[2])
	}
	window.StartMin, err = strconv.Atoi(matches[3])
	if err != nil || window.StartMin > 59 {
		return nil, fmt.Errorf("invalid start minute: %s", matches[3])
	}
	window.EndHour, err = strconv.Atoi(matches[4])
	if err != nil || window.EndHour > 23 {
		return nil, fmt.Errorf("invalid end hour: %s", matches[4])
	}
	window.EndMin, err = strconv.Atoi(matches[5])
	if err != nil || window.EndMin > 59 {
		return nil, fmt.Errorf("invalid end minute: %s", matches[5])
	}

	return window, nil
}

// IsInWindow checks if the current time is within the reboot window
func (w *RebootWindow) IsInWindow(t time.Time) bool {
	if w == nil {
		return true // No window restriction
	}

	// Check day of week (if specified)
	if w.DayOfWeek >= 0 && t.Weekday() != w.DayOfWeek {
		return false
	}

	// Check time range
	currentMinutes := t.Hour()*60 + t.Minute()
	startMinutes := w.StartHour*60 + w.StartMin
	endMinutes := w.EndHour*60 + w.EndMin

	// Handle overnight windows (e.g., 23:00-02:00)
	if endMinutes < startMinutes {
		return currentMinutes >= startMinutes || currentMinutes < endMinutes
	}

	return currentMinutes >= startMinutes && currentMinutes < endMinutes
}

// NextWindowStart returns when the next reboot window starts
func (w *RebootWindow) NextWindowStart(from time.Time) time.Time {
	if w == nil {
		return from
	}

	// Start of current day's window
	windowStart := time.Date(from.Year(), from.Month(), from.Day(), w.StartHour, w.StartMin, 0, 0, from.Location())

	// If we're past today's window, move to tomorrow
	if from.After(windowStart) {
		windowStart = windowStart.AddDate(0, 0, 1)
	}

	// If specific day of week, advance to that day
	if w.DayOfWeek >= 0 {
		for windowStart.Weekday() != w.DayOfWeek {
			windowStart = windowStart.AddDate(0, 0, 1)
		}
	}

	return windowStart
}

// ExecuteReboot orchestrates a reboot for a single host
func (o *Orchestrator) ExecuteReboot(ctx context.Context, client *ssh.Client, pool *ssh.Pool, host string, port int, user string) error {
	// Check if reboot is allowed
	if !o.config.AllowReboot {
		return fmt.Errorf("reboot not allowed by configuration")
	}

	// Check reboot window
	if o.config.Window != nil && !o.config.Window.IsInWindow(time.Now()) {
		next := o.config.Window.NextWindowStart(time.Now())
		return fmt.Errorf("outside reboot window, next window starts at %s", next.Format(time.RFC3339))
	}

	// Run pre-reboot hook
	if o.config.PreRebootHook != "" {
		result, err := client.ExecSudo(ctx, o.config.PreRebootHook)
		if err != nil {
			return fmt.Errorf("pre-reboot hook failed: %w", err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("pre-reboot hook failed: %s", result.Stderr)
		}
	}

	// Initiate reboot
	// Use shutdown to schedule reboot in 1 minute and give us time to close connection
	_, err := client.ExecSudo(ctx, "shutdown -r +1 'NixFleet scheduled reboot'")
	if err != nil {
		return fmt.Errorf("failed to schedule reboot: %w", err)
	}

	// Close current connection before reboot
	pool.Remove(host, port)

	// Wait for host to go down
	time.Sleep(70 * time.Second) // Wait for reboot to start

	// Wait for host to come back up
	return o.waitForHost(ctx, pool, host, port, user)
}

// waitForHost waits for a host to become reachable after reboot
func (o *Orchestrator) waitForHost(ctx context.Context, pool *ssh.Pool, host string, port int, user string) error {
	deadline := time.Now().Add(o.config.WaitTimeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Try to connect
		client, err := pool.GetWithUser(ctx, host, port, user)
		if err == nil {
			// Connection successful, verify host is responsive
			result, err := client.Exec(ctx, "echo 'reboot-complete'")
			if err == nil && strings.TrimSpace(result.Stdout) == "reboot-complete" {
				return nil
			}
		}

		// Wait before retry
		time.Sleep(o.config.WaitInterval)
	}

	return fmt.Errorf("host did not come back up within %v", o.config.WaitTimeout)
}

// RunPostRebootHook runs the post-reboot hook on a host
func (o *Orchestrator) RunPostRebootHook(ctx context.Context, client *ssh.Client) error {
	if o.config.PostRebootHook == "" {
		return nil
	}

	result, err := client.ExecSudo(ctx, o.config.PostRebootHook)
	if err != nil {
		return fmt.Errorf("post-reboot hook failed: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("post-reboot hook failed: %s", result.Stderr)
	}

	return nil
}

// ConcurrencyLimiter manages concurrent reboots
type ConcurrencyLimiter struct {
	max     int
	current int
	done    chan struct{}
}

// NewConcurrencyLimiter creates a new limiter
func NewConcurrencyLimiter(max int) *ConcurrencyLimiter {
	if max < 1 {
		max = 1
	}
	return &ConcurrencyLimiter{
		max:  max,
		done: make(chan struct{}, max),
	}
}

// Acquire waits until a slot is available
func (l *ConcurrencyLimiter) Acquire(ctx context.Context) error {
	if l.current < l.max {
		l.current++
		return nil
	}

	select {
	case <-l.done:
		l.current++
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release releases a slot
func (l *ConcurrencyLimiter) Release() {
	l.current--
	select {
	case l.done <- struct{}{}:
	default:
	}
}

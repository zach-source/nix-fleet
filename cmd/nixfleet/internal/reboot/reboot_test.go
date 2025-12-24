package reboot

import (
	"context"
	"testing"
	"time"
)

func TestParseRebootWindow(t *testing.T) {
	tests := []struct {
		input    string
		wantErr  bool
		dayOfWk  time.Weekday
		startHr  int
		startMin int
		endHr    int
		endMin   int
	}{
		{"Sun 02:00-04:00", false, time.Sunday, 2, 0, 4, 0},
		{"Mon 00:00-06:00", false, time.Monday, 0, 0, 6, 0},
		{"Tue 22:00-23:59", false, time.Tuesday, 22, 0, 23, 59},
		{"Wed 12:00-14:00", false, time.Wednesday, 12, 0, 14, 0},
		{"Thu 03:00-05:00", false, time.Thursday, 3, 0, 5, 0},
		{"Fri 01:00-02:00", false, time.Friday, 1, 0, 2, 0},
		{"Sat 04:00-08:00", false, time.Saturday, 4, 0, 8, 0},
		{"invalid", true, 0, 0, 0, 0, 0},
		{"Sun 25:00-26:00", true, 0, 0, 0, 0, 0},
		{"", false, 0, 0, 0, 0, 0}, // Empty returns nil, nil
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			window, err := ParseRebootWindow(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRebootWindow(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if window == nil {
				if tt.input != "" {
					t.Error("Expected window for non-empty input")
				}
				return
			}
			if window.DayOfWeek != tt.dayOfWk {
				t.Errorf("DayOfWeek = %v, want %v", window.DayOfWeek, tt.dayOfWk)
			}
			if window.StartHour != tt.startHr {
				t.Errorf("StartHour = %d, want %d", window.StartHour, tt.startHr)
			}
			if window.StartMin != tt.startMin {
				t.Errorf("StartMin = %d, want %d", window.StartMin, tt.startMin)
			}
			if window.EndHour != tt.endHr {
				t.Errorf("EndHour = %d, want %d", window.EndHour, tt.endHr)
			}
			if window.EndMin != tt.endMin {
				t.Errorf("EndMin = %d, want %d", window.EndMin, tt.endMin)
			}
		})
	}
}

func TestRebootWindowIsInWindow(t *testing.T) {
	window := &RebootWindow{
		DayOfWeek: time.Sunday,
		StartHour: 2,
		StartMin:  0,
		EndHour:   4,
		EndMin:    0,
	}

	tests := []struct {
		name     string
		time     time.Time
		expected bool
	}{
		{
			name:     "inside window",
			time:     time.Date(2024, 1, 7, 3, 0, 0, 0, time.UTC), // Sunday 03:00
			expected: true,
		},
		{
			name:     "at window start",
			time:     time.Date(2024, 1, 7, 2, 0, 0, 0, time.UTC), // Sunday 02:00
			expected: true,
		},
		{
			name:     "before window end",
			time:     time.Date(2024, 1, 7, 3, 59, 0, 0, time.UTC), // Sunday 03:59
			expected: true,
		},
		{
			name:     "after window end",
			time:     time.Date(2024, 1, 7, 4, 1, 0, 0, time.UTC), // Sunday 04:01
			expected: false,
		},
		{
			name:     "before window start",
			time:     time.Date(2024, 1, 7, 1, 59, 0, 0, time.UTC), // Sunday 01:59
			expected: false,
		},
		{
			name:     "wrong day",
			time:     time.Date(2024, 1, 8, 3, 0, 0, 0, time.UTC), // Monday 03:00
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := window.IsInWindow(tt.time)
			if result != tt.expected {
				t.Errorf("IsInWindow(%v) = %v, want %v", tt.time, result, tt.expected)
			}
		})
	}
}

func TestRebootWindowIsInWindowNil(t *testing.T) {
	var window *RebootWindow = nil
	// Nil window should always return true (no restriction)
	if !window.IsInWindow(time.Now()) {
		t.Error("Nil window should always return true")
	}
}

func TestNewOrchestrator(t *testing.T) {
	config := DefaultRebootConfig()
	orch := NewOrchestrator(config)

	if orch == nil {
		t.Error("NewOrchestrator should not return nil")
	}
}

func TestDefaultRebootConfig(t *testing.T) {
	config := DefaultRebootConfig()

	if config.AllowReboot {
		t.Error("AllowReboot should default to false")
	}
	if config.MaxConcurrentReboots != 1 {
		t.Errorf("MaxConcurrentReboots = %d, want 1", config.MaxConcurrentReboots)
	}
	if config.WaitTimeout != 10*time.Minute {
		t.Errorf("WaitTimeout = %v, want 10m", config.WaitTimeout)
	}
	if config.WaitInterval != 10*time.Second {
		t.Errorf("WaitInterval = %v, want 10s", config.WaitInterval)
	}
	if config.Window != nil {
		t.Error("Window should default to nil")
	}
	if config.PreRebootHook != "" {
		t.Error("PreRebootHook should default to empty")
	}
	if config.PostRebootHook != "" {
		t.Error("PostRebootHook should default to empty")
	}
}

func TestRebootConfig(t *testing.T) {
	window, _ := ParseRebootWindow("Sun 02:00-04:00")

	config := RebootConfig{
		AllowReboot:          true,
		Window:               window,
		MaxConcurrentReboots: 2,
		PreRebootHook:        "/etc/nixfleet/pre-reboot.sh",
		PostRebootHook:       "/etc/nixfleet/post-reboot.sh",
		WaitTimeout:          15 * time.Minute,
		WaitInterval:         5 * time.Second,
	}

	if !config.AllowReboot {
		t.Error("AllowReboot should be true")
	}
	if config.Window == nil {
		t.Error("Window should not be nil")
	}
	if config.MaxConcurrentReboots != 2 {
		t.Errorf("MaxConcurrentReboots = %d, want 2", config.MaxConcurrentReboots)
	}
	if config.PreRebootHook != "/etc/nixfleet/pre-reboot.sh" {
		t.Error("PreRebootHook mismatch")
	}
}

func TestRebootStatus(t *testing.T) {
	status := &RebootStatus{
		Required:        true,
		Reason:          "kernel update",
		TriggerPackages: []string{"linux-kernel-5.4"},
	}

	if !status.Required {
		t.Error("Required should be true")
	}
	if status.Reason != "kernel update" {
		t.Errorf("Reason = %s, want 'kernel update'", status.Reason)
	}
	if len(status.TriggerPackages) != 1 {
		t.Errorf("TriggerPackages count = %d, want 1", len(status.TriggerPackages))
	}
}

func TestConcurrencyLimiter(t *testing.T) {
	limiter := NewConcurrencyLimiter(2)

	// First two acquires should succeed
	ctx := context.Background()
	if err := limiter.Acquire(ctx); err != nil {
		t.Errorf("First acquire failed: %v", err)
	}
	if err := limiter.Acquire(ctx); err != nil {
		t.Errorf("Second acquire failed: %v", err)
	}

	// Release one
	limiter.Release()

	// Should be able to acquire again
	if err := limiter.Acquire(ctx); err != nil {
		t.Errorf("Third acquire failed after release: %v", err)
	}
}

func TestConcurrencyLimiterCancel(t *testing.T) {
	limiter := NewConcurrencyLimiter(1)

	ctx := context.Background()
	_ = limiter.Acquire(ctx) // Take the only slot

	// Create a context that will be cancelled
	cancelCtx, cancel := context.WithCancel(context.Background())

	// Try to acquire in goroutine
	done := make(chan error, 1)
	go func() {
		done <- limiter.Acquire(cancelCtx)
	}()

	// Cancel the context
	cancel()

	// Should get an error
	err := <-done
	if err == nil {
		t.Error("Expected error from cancelled context")
	}
}

func TestConcurrencyLimiterMinMax(t *testing.T) {
	// Even with 0 or negative, should use minimum of 1
	limiter := NewConcurrencyLimiter(0)
	if limiter.max != 1 {
		t.Errorf("Expected max to be 1 for 0 input, got %d", limiter.max)
	}

	limiter = NewConcurrencyLimiter(-5)
	if limiter.max != 1 {
		t.Errorf("Expected max to be 1 for negative input, got %d", limiter.max)
	}
}

func TestRebootWindowNextWindowStart(t *testing.T) {
	window := &RebootWindow{
		DayOfWeek: time.Sunday,
		StartHour: 2,
		StartMin:  0,
		EndHour:   4,
		EndMin:    0,
	}

	// Get a reference time (Wednesday)
	now := time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC) // Wednesday noon

	next := window.NextWindowStart(now)

	// Next Sunday should be Jan 14, 2024 at 02:00
	if next.Weekday() != time.Sunday {
		t.Errorf("Expected Sunday, got %v", next.Weekday())
	}
	if next.Hour() != 2 {
		t.Errorf("Expected hour 2, got %d", next.Hour())
	}
	if next.Minute() != 0 {
		t.Errorf("Expected minute 0, got %d", next.Minute())
	}
}

func TestRebootWindowNextWindowStartNil(t *testing.T) {
	var window *RebootWindow = nil
	now := time.Now()

	next := window.NextWindowStart(now)
	if !next.Equal(now) {
		t.Error("Nil window should return input time")
	}
}

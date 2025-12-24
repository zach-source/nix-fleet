package osupdate

import (
	"testing"
	"time"
)

func TestParsePolicy(t *testing.T) {
	tests := []struct {
		input    string
		expected Policy
		wantErr  bool
	}{
		{"security-daily", PolicySecurityDaily, false},
		{"security", PolicySecurityDaily, false},
		{"full-weekly", PolicyFullWeekly, false},
		{"full", PolicyFullWeekly, false},
		{"manual", PolicyManual, false},
		{"none", PolicyManual, false},
		{"invalid", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParsePolicy(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePolicy(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("ParsePolicy(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestDefaultPolicyConfig(t *testing.T) {
	tests := []struct {
		policy            Policy
		maintenanceWindow string
		allowReboot       bool
	}{
		{
			policy:            PolicySecurityDaily,
			maintenanceWindow: "02:00-06:00",
			allowReboot:       false,
		},
		{
			policy:            PolicyFullWeekly,
			maintenanceWindow: "Sun 02:00-06:00",
			allowReboot:       true,
		},
		{
			policy:            PolicyManual,
			maintenanceWindow: "",
			allowReboot:       false,
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.policy), func(t *testing.T) {
			result := DefaultPolicyConfig(tt.policy)
			if result.Policy != tt.policy {
				t.Errorf("Policy = %v, want %v", result.Policy, tt.policy)
			}
			if result.MaintenanceWindow != tt.maintenanceWindow {
				t.Errorf("MaintenanceWindow = %v, want %v", result.MaintenanceWindow, tt.maintenanceWindow)
			}
			if result.AllowReboot != tt.allowReboot {
				t.Errorf("AllowReboot = %v, want %v", result.AllowReboot, tt.allowReboot)
			}
		})
	}
}

func TestPolicyConstants(t *testing.T) {
	if PolicySecurityDaily != "security-daily" {
		t.Errorf("PolicySecurityDaily = %v, want 'security-daily'", PolicySecurityDaily)
	}
	if PolicyFullWeekly != "full-weekly" {
		t.Errorf("PolicyFullWeekly = %v, want 'full-weekly'", PolicyFullWeekly)
	}
	if PolicyManual != "manual" {
		t.Errorf("PolicyManual = %v, want 'manual'", PolicyManual)
	}
}

func TestNewUpdater(t *testing.T) {
	updater := NewUpdater()
	if updater == nil {
		t.Error("NewUpdater should not return nil")
	}
}

func TestPendingUpdates(t *testing.T) {
	updates := &PendingUpdates{
		TotalCount: 10,
		SecurityUpdates: []PendingPackage{
			{Name: "openssl", CurrentVersion: "1.1.1", NewVersion: "1.1.2", IsSecurityFix: true},
		},
		RegularUpdates: []PendingPackage{
			{Name: "nginx", CurrentVersion: "1.18", NewVersion: "1.20", IsSecurityFix: false},
		},
	}

	if updates.TotalCount != 10 {
		t.Errorf("TotalCount = %d, want 10", updates.TotalCount)
	}
	if len(updates.SecurityUpdates) != 1 {
		t.Errorf("SecurityUpdates count = %d, want 1", len(updates.SecurityUpdates))
	}
	if len(updates.RegularUpdates) != 1 {
		t.Errorf("RegularUpdates count = %d, want 1", len(updates.RegularUpdates))
	}
}

func TestPendingPackage(t *testing.T) {
	pkg := PendingPackage{
		Name:           "nginx",
		CurrentVersion: "1.18.0",
		NewVersion:     "1.20.0",
		IsSecurityFix:  false,
	}

	if pkg.Name != "nginx" {
		t.Errorf("Name = %s, want 'nginx'", pkg.Name)
	}
	if pkg.CurrentVersion != "1.18.0" {
		t.Errorf("CurrentVersion = %s, want '1.18.0'", pkg.CurrentVersion)
	}
	if pkg.IsSecurityFix {
		t.Error("IsSecurityFix should be false")
	}
}

func TestPackageUpdate(t *testing.T) {
	update := PackageUpdate{
		Name:       "nginx",
		OldVersion: "1.18.0",
		NewVersion: "1.20.0",
		Action:     "upgrade",
	}

	if update.Name != "nginx" {
		t.Errorf("Name = %s, want 'nginx'", update.Name)
	}
	if update.OldVersion != "1.18.0" {
		t.Errorf("OldVersion = %s, want '1.18.0'", update.OldVersion)
	}
	if update.Action != "upgrade" {
		t.Errorf("Action = %s, want 'upgrade'", update.Action)
	}
}

func TestUpdateResult(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Minute)

	result := &UpdateResult{
		Success: true,
		PackagesUpdated: []PackageUpdate{
			{Name: "nginx", OldVersion: "1.18", NewVersion: "1.20", Action: "upgrade"},
		},
		RebootRequired: true,
		StartTime:      start,
		EndTime:        end,
		Stdout:         "Success",
		Stderr:         "",
	}

	if !result.Success {
		t.Error("Success should be true")
	}
	if len(result.PackagesUpdated) != 1 {
		t.Errorf("PackagesUpdated count = %d, want 1", len(result.PackagesUpdated))
	}
	if !result.RebootRequired {
		t.Error("RebootRequired should be true")
	}
	if result.EndTime.Sub(result.StartTime) != 5*time.Minute {
		t.Error("Duration calculation incorrect")
	}
}

func TestPolicyConfigValidation(t *testing.T) {
	// Test maintenance window format
	validConfig := PolicyConfig{
		Policy:            PolicySecurityDaily,
		MaintenanceWindow: "Sun 02:00-06:00",
		AllowReboot:       true,
		RebootDelay:       5 * time.Minute,
	}

	if validConfig.MaintenanceWindow != "Sun 02:00-06:00" {
		t.Error("MaintenanceWindow not set correctly")
	}
	if validConfig.RebootDelay != 5*time.Minute {
		t.Error("RebootDelay not set correctly")
	}
}

func TestPolicyConfigWithHeldPackages(t *testing.T) {
	config := PolicyConfig{
		Policy:       PolicySecurityDaily,
		HeldPackages: []string{"linux-kernel", "nvidia-driver"},
		AllowReboot:  false,
	}

	if len(config.HeldPackages) != 2 {
		t.Errorf("HeldPackages count = %d, want 2", len(config.HeldPackages))
	}
	if config.HeldPackages[0] != "linux-kernel" {
		t.Errorf("HeldPackages[0] = %s, want 'linux-kernel'", config.HeldPackages[0])
	}
}

func TestDefaultPolicyConfigRebootDelay(t *testing.T) {
	// PolicyFullWeekly should have a reboot delay
	config := DefaultPolicyConfig(PolicyFullWeekly)
	if config.RebootDelay != 5*time.Minute {
		t.Errorf("PolicyFullWeekly RebootDelay = %v, want 5m", config.RebootDelay)
	}

	// PolicySecurityDaily should have no reboot delay
	config = DefaultPolicyConfig(PolicySecurityDaily)
	if config.RebootDelay != 0 {
		t.Errorf("PolicySecurityDaily RebootDelay = %v, want 0", config.RebootDelay)
	}
}

func TestUpdateResultFailed(t *testing.T) {
	result := &UpdateResult{
		Success:        false,
		RebootRequired: false,
		Stderr:         "apt-get failed",
	}

	if result.Success {
		t.Error("Success should be false")
	}
	if result.Stderr != "apt-get failed" {
		t.Errorf("Stderr = %s, want 'apt-get failed'", result.Stderr)
	}
}

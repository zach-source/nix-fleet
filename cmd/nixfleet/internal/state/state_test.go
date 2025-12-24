package state

import (
	"testing"
	"time"
)

func TestNewHostState(t *testing.T) {
	state := NewHostState("web1", "ubuntu")

	if state.Hostname != "web1" {
		t.Errorf("Expected hostname 'web1', got '%s'", state.Hostname)
	}
	if state.Base != "ubuntu" {
		t.Errorf("Expected base 'ubuntu', got '%s'", state.Base)
	}
	if state.ServiceHealth == nil {
		t.Error("ServiceHealth map should not be nil")
	}
	if state.ManagedFiles == nil {
		t.Error("ManagedFiles map should not be nil")
	}
	if state.StateVersion != 1 {
		t.Errorf("Expected StateVersion 1, got %d", state.StateVersion)
	}
	if state.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}

func TestNewManager(t *testing.T) {
	mgr := NewManager()
	if mgr == nil {
		t.Error("NewManager should not return nil")
	}
}

func TestDriftResultHasDrift(t *testing.T) {
	tests := []struct {
		name     string
		status   DriftStatus
		expected bool
	}{
		{"OK status", DriftStatusOK, false},
		{"Missing status", DriftStatusMissing, true},
		{"Content changed", DriftStatusContentChanged, true},
		{"Permissions changed", DriftStatusPermissionsChanged, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DriftResult{Status: tt.status}
			if result.HasDrift() != tt.expected {
				t.Errorf("HasDrift() = %v, expected %v", result.HasDrift(), tt.expected)
			}
		})
	}
}

func TestPlanDiffHasChanges(t *testing.T) {
	tests := []struct {
		name     string
		diff     PlanDiff
		expected bool
	}{
		{
			name:     "empty diff",
			diff:     PlanDiff{},
			expected: false,
		},
		{
			name:     "files changed",
			diff:     PlanDiff{FilesChanged: []FileDiff{{Path: "/etc/test"}}},
			expected: true,
		},
		{
			name:     "files added",
			diff:     PlanDiff{FilesAdded: []string{"/etc/new"}},
			expected: true,
		},
		{
			name:     "files removed",
			diff:     PlanDiff{FilesRemoved: []string{"/etc/old"}},
			expected: true,
		},
		{
			name:     "units to restart",
			diff:     PlanDiff{UnitsToRestart: []string{"nginx.service"}},
			expected: true,
		},
		{
			name:     "packages to add",
			diff:     PlanDiff{PackagesToAdd: []string{"nginx"}},
			expected: true,
		},
		{
			name:     "packages to remove",
			diff:     PlanDiff{PackagesToRemove: []string{"apache2"}},
			expected: true,
		},
		{
			name:     "users to add",
			diff:     PlanDiff{UsersToAdd: []string{"deploy"}},
			expected: true,
		},
		{
			name:     "users to remove",
			diff:     PlanDiff{UsersToRemove: []string{"olduser"}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.diff.HasChanges() != tt.expected {
				t.Errorf("HasChanges() = %v, expected %v", tt.diff.HasChanges(), tt.expected)
			}
		})
	}
}

func TestPlanDiffSummary(t *testing.T) {
	// Empty diff
	emptyDiff := PlanDiff{}
	summary := emptyDiff.Summary()
	if summary != "No changes detected.\n" {
		t.Errorf("Expected 'No changes detected.', got '%s'", summary)
	}

	// Diff with changes
	diff := PlanDiff{
		FilesAdded:   []string{"/etc/new.conf"},
		FilesChanged: []FileDiff{{Path: "/etc/changed.conf"}},
		FilesRemoved: []string{"/etc/old.conf"},
	}
	summary = diff.Summary()

	if len(summary) == 0 {
		t.Error("Expected non-empty summary")
	}
	// Check that it contains expected content
	if !containsString(summary, "/etc/new.conf") {
		t.Error("Summary should contain added file")
	}
	if !containsString(summary, "/etc/changed.conf") {
		t.Error("Summary should contain changed file")
	}
	if !containsString(summary, "/etc/old.conf") {
		t.Error("Summary should contain removed file")
	}
}

func TestHostStateGetHostSummary(t *testing.T) {
	state := &HostState{
		Hostname:          "web1",
		Base:              "ubuntu",
		CurrentGeneration: 5,
		LastApply:         time.Now(),
		RebootRequired:    true,
		RebootPackages:    []string{"linux-kernel"},
		PendingUpdates:    10,
		SecurityUpdates:   2,
		DriftDetected:     true,
		DriftFiles:        []string{"/etc/nginx/nginx.conf"},
		ServiceHealth: map[string]ServiceStatus{
			"nginx": {Active: true},
			"mysql": {Active: false},
		},
	}

	summary := state.GetHostSummary()

	if !containsString(summary, "web1") {
		t.Error("Summary should contain hostname")
	}
	if !containsString(summary, "ubuntu") {
		t.Error("Summary should contain base")
	}
	if !containsString(summary, "Generation: 5") {
		t.Error("Summary should contain generation")
	}
	if !containsString(summary, "Reboot Required: YES") {
		t.Error("Summary should indicate reboot required")
	}
	if !containsString(summary, "linux-kernel") {
		t.Error("Summary should contain reboot packages")
	}
	if !containsString(summary, "Pending Updates: 10") {
		t.Error("Summary should contain pending updates")
	}
	if !containsString(summary, "Drift Detected") {
		t.Error("Summary should indicate drift detected")
	}
	if !containsString(summary, "1 healthy") {
		t.Error("Summary should contain healthy services count")
	}
	if !containsString(summary, "1 unhealthy") {
		t.Error("Summary should contain unhealthy services count")
	}
}

func TestDriftStatusConstants(t *testing.T) {
	// Verify constants are defined correctly
	if DriftStatusOK != "ok" {
		t.Errorf("Expected DriftStatusOK to be 'ok', got '%s'", DriftStatusOK)
	}
	if DriftStatusMissing != "missing" {
		t.Errorf("Expected DriftStatusMissing to be 'missing', got '%s'", DriftStatusMissing)
	}
	if DriftStatusContentChanged != "content_changed" {
		t.Errorf("Expected DriftStatusContentChanged to be 'content_changed', got '%s'", DriftStatusContentChanged)
	}
	if DriftStatusPermissionsChanged != "permissions_changed" {
		t.Errorf("Expected DriftStatusPermissionsChanged to be 'permissions_changed', got '%s'", DriftStatusPermissionsChanged)
	}
}

func TestStatePathConstants(t *testing.T) {
	if StatePath != "/var/lib/nixfleet/state.json" {
		t.Errorf("Expected StatePath to be '/var/lib/nixfleet/state.json', got '%s'", StatePath)
	}
	if StateDir != "/var/lib/nixfleet" {
		t.Errorf("Expected StateDir to be '/var/lib/nixfleet', got '%s'", StateDir)
	}
}

func TestHashContent(t *testing.T) {
	// Test that same content produces same hash
	content1 := []byte("test content")
	content2 := []byte("test content")
	content3 := []byte("different content")

	hash1 := hashContent(content1)
	hash2 := hashContent(content2)
	hash3 := hashContent(content3)

	if hash1 != hash2 {
		t.Error("Same content should produce same hash")
	}
	if hash1 == hash3 {
		t.Error("Different content should produce different hash")
	}
	if len(hash1) != 64 { // SHA256 hex length
		t.Errorf("Expected hash length 64, got %d", len(hash1))
	}
}

func TestFileState(t *testing.T) {
	fs := FileState{
		Path:         "/etc/nginx/nginx.conf",
		Hash:         "abc123",
		Mode:         "644",
		Owner:        "root",
		Group:        "root",
		RestartUnits: []string{"nginx.service"},
	}

	if fs.Path != "/etc/nginx/nginx.conf" {
		t.Errorf("Unexpected path: %s", fs.Path)
	}
	if fs.Mode != "644" {
		t.Errorf("Unexpected mode: %s", fs.Mode)
	}
	if len(fs.RestartUnits) != 1 {
		t.Errorf("Expected 1 restart unit, got %d", len(fs.RestartUnits))
	}
}

func TestServiceStatus(t *testing.T) {
	now := time.Now()
	ss := ServiceStatus{
		Active:    true,
		Enabled:   true,
		SubState:  "running",
		LastCheck: now,
	}

	if !ss.Active {
		t.Error("Expected Active to be true")
	}
	if !ss.Enabled {
		t.Error("Expected Enabled to be true")
	}
	if ss.SubState != "running" {
		t.Errorf("Expected SubState 'running', got '%s'", ss.SubState)
	}
}

func TestPackageDiff(t *testing.T) {
	pd := PackageDiff{
		Name:       "nginx",
		OldVersion: "1.18.0",
		NewVersion: "1.20.0",
		Action:     "upgrade",
	}

	if pd.Name != "nginx" {
		t.Errorf("Unexpected name: %s", pd.Name)
	}
	if pd.Action != "upgrade" {
		t.Errorf("Unexpected action: %s", pd.Action)
	}
}

func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || containsString(s[1:], substr)))
}

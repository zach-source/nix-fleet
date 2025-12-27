// Package state implements host state management and drift detection for NixFleet
package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

const (
	// StatePath is the default path for state file on hosts
	StatePath = "/var/lib/nixfleet/state.json"
	// StateDir is the directory containing state
	StateDir = "/var/lib/nixfleet"
)

// OSInfo contains operating system information
type OSInfo struct {
	Name         string `json:"name"`         // e.g., "Ubuntu"
	Version      string `json:"version"`      // e.g., "24.04"
	VersionID    string `json:"version_id"`   // e.g., "24.04"
	PrettyName   string `json:"pretty_name"`  // e.g., "Ubuntu 24.04.1 LTS"
	Codename     string `json:"codename"`     // e.g., "noble"
	Kernel       string `json:"kernel"`       // e.g., "6.8.0-45-generic"
	Architecture string `json:"architecture"` // e.g., "x86_64"
	Uptime       string `json:"uptime"`       // e.g., "5 days, 3:22"
	LastBoot     string `json:"last_boot"`    // e.g., "2024-12-18 10:30:00"
}

// HostState represents the current state of a managed host
type HostState struct {
	// Identity
	Hostname string `json:"hostname"`
	Base     string `json:"base"` // ubuntu, nixos, darwin

	// OS Information
	OSInfo *OSInfo `json:"os_info,omitempty"`

	// Current deployment
	CurrentGeneration int       `json:"current_generation"`
	ManifestHash      string    `json:"manifest_hash"`
	StorePath         string    `json:"store_path"`
	LastApply         time.Time `json:"last_apply"`
	ApplyDuration     string    `json:"apply_duration"`

	// OS Updates
	LastOSUpdate      time.Time     `json:"last_os_update,omitempty"`
	PendingUpdates    int           `json:"pending_updates"`
	SecurityUpdates   int           `json:"security_updates"`
	LastUpdateCheck   time.Time     `json:"last_update_check,omitempty"`
	UpdatePackageDiff []PackageDiff `json:"update_package_diff,omitempty"`

	// Reboot status
	RebootRequired bool      `json:"reboot_required"`
	RebootPackages []string  `json:"reboot_packages,omitempty"`
	LastReboot     time.Time `json:"last_reboot,omitempty"`

	// Service health
	ServiceHealth map[string]ServiceStatus `json:"service_health,omitempty"`

	// Managed files
	ManagedFiles map[string]FileState `json:"managed_files,omitempty"`

	// Drift detection
	DriftDetected  bool      `json:"drift_detected"`
	DriftFiles     []string  `json:"drift_files,omitempty"`
	LastDriftCheck time.Time `json:"last_drift_check,omitempty"`

	// k0s Kubernetes state (for reconciliation)
	K0s *K0sState `json:"k0s,omitempty"`

	// Metadata
	NixFleetVersion string    `json:"nixfleet_version"`
	StateVersion    int       `json:"state_version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// K0sState tracks deployed k0s resources for reconciliation
// This enables automatic cleanup of orphaned resources when config changes
type K0sState struct {
	// Enabled indicates if k0s is configured on this host
	Enabled bool `json:"enabled"`

	// ConfigHash is the hash of k0s.yaml for change detection
	ConfigHash string `json:"config_hash,omitempty"`

	// HelmCharts tracks Helm releases deployed via k0s extensions
	HelmCharts []K0sHelmChartState `json:"helm_charts,omitempty"`

	// Manifests tracks resources deployed via /var/lib/k0s/manifests/
	Manifests []K0sManifestState `json:"manifests,omitempty"`

	// LastReconcile is when resources were last reconciled
	LastReconcile time.Time `json:"last_reconcile,omitempty"`
}

// K0sHelmChartState tracks a Helm chart deployed via k0s
type K0sHelmChartState struct {
	// Name is the release name (e.g., "cilium", "cert-manager")
	Name string `json:"name"`

	// Namespace where the chart is deployed
	Namespace string `json:"namespace"`

	// ChartName is the chart reference (e.g., "cilium/cilium")
	ChartName string `json:"chart_name"`

	// Version of the chart
	Version string `json:"version"`
}

// K0sManifestState tracks a resource deployed via k0s manifests
type K0sManifestState struct {
	// LogicalName is the stable identifier from nix config (e.g., "lb-pool", "fleet-ca-issuer")
	LogicalName string `json:"logical_name"`

	// Kind is the Kubernetes resource kind (e.g., "CiliumLoadBalancerIPPool")
	Kind string `json:"kind"`

	// APIVersion is the API version (e.g., "cilium.io/v2alpha1")
	APIVersion string `json:"api_version"`

	// Name is the Kubernetes resource name
	Name string `json:"name"`

	// Namespace is empty for cluster-scoped resources
	Namespace string `json:"namespace,omitempty"`

	// ManifestFile is the source manifest file (e.g., "cilium-lb-pool.yaml")
	ManifestFile string `json:"manifest_file"`
}

// PackageDiff represents a package version change
type PackageDiff struct {
	Name       string `json:"name"`
	OldVersion string `json:"old_version"`
	NewVersion string `json:"new_version"`
	Action     string `json:"action"` // upgrade, install, remove
}

// ServiceStatus represents the status of a systemd service
type ServiceStatus struct {
	Active    bool      `json:"active"`
	Enabled   bool      `json:"enabled"`
	SubState  string    `json:"sub_state"`
	LastCheck time.Time `json:"last_check"`
}

// FileState represents the state of a managed file
type FileState struct {
	Path         string   `json:"path"`
	Hash         string   `json:"hash"`
	Mode         string   `json:"mode"`
	Owner        string   `json:"owner"`
	Group        string   `json:"group"`
	RestartUnits []string `json:"restart_units,omitempty"`
}

// NewHostState creates a new empty host state
func NewHostState(hostname, base string) *HostState {
	return &HostState{
		Hostname:      hostname,
		Base:          base,
		ServiceHealth: make(map[string]ServiceStatus),
		ManagedFiles:  make(map[string]FileState),
		StateVersion:  1,
		UpdatedAt:     time.Now(),
	}
}

// Manager handles state operations on remote hosts
type Manager struct{}

// NewManager creates a new state manager
func NewManager() *Manager {
	return &Manager{}
}

// ReadState reads the current state from a host
func (m *Manager) ReadState(ctx context.Context, client *ssh.Client) (*HostState, error) {
	// Ensure state directory exists
	result, err := client.Exec(ctx, fmt.Sprintf("cat %s 2>/dev/null || echo '{}'", StatePath))
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}

	var state HostState
	if err := json.Unmarshal([]byte(result.Stdout), &state); err != nil {
		// Return empty state if parsing fails
		return &HostState{
			ServiceHealth: make(map[string]ServiceStatus),
			ManagedFiles:  make(map[string]FileState),
			StateVersion:  1,
		}, nil
	}

	return &state, nil
}

// WriteState writes state to a host
func (m *Manager) WriteState(ctx context.Context, client *ssh.Client, state *HostState) error {
	state.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	// Ensure directory exists
	mkdirCmd := fmt.Sprintf("mkdir -p %s", StateDir)
	if _, err := client.ExecSudo(ctx, mkdirCmd); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	// Write state file
	writeCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", StatePath, string(data))
	result, err := client.ExecSudo(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("writing state failed: %s", result.Stderr)
	}

	return nil
}

// UpdateAfterApply updates state after a successful apply
func (m *Manager) UpdateAfterApply(ctx context.Context, client *ssh.Client, storePath, manifestHash string, generation int, duration time.Duration) error {
	state, err := m.ReadState(ctx, client)
	if err != nil {
		state = NewHostState("", "")
	}

	state.StorePath = storePath
	state.ManifestHash = manifestHash
	state.CurrentGeneration = generation
	state.LastApply = time.Now()
	state.ApplyDuration = duration.String()

	return m.WriteState(ctx, client, state)
}

// UpdateRebootStatus updates the reboot status in state
func (m *Manager) UpdateRebootStatus(ctx context.Context, client *ssh.Client, required bool, packages []string) error {
	state, err := m.ReadState(ctx, client)
	if err != nil {
		state = NewHostState("", "")
	}

	state.RebootRequired = required
	state.RebootPackages = packages

	return m.WriteState(ctx, client, state)
}

// UpdateServiceHealth updates service health status
func (m *Manager) UpdateServiceHealth(ctx context.Context, client *ssh.Client, services map[string]ServiceStatus) error {
	state, err := m.ReadState(ctx, client)
	if err != nil {
		state = NewHostState("", "")
	}

	state.ServiceHealth = services

	return m.WriteState(ctx, client, state)
}

// CheckDrift compares managed files against their expected state
func (m *Manager) CheckDrift(ctx context.Context, client *ssh.Client, expectedFiles map[string]FileState) ([]DriftResult, error) {
	var results []DriftResult

	for path, expected := range expectedFiles {
		result := DriftResult{
			Path:     path,
			Expected: expected,
		}

		// Get current file hash
		hashCmd := fmt.Sprintf("sha256sum %s 2>/dev/null | cut -d' ' -f1", path)
		hashResult, err := client.Exec(ctx, hashCmd)
		if err != nil || hashResult.ExitCode != 0 {
			result.Status = DriftStatusMissing
			results = append(results, result)
			continue
		}

		currentHash := strings.TrimSpace(hashResult.Stdout)
		result.Actual.Hash = currentHash

		// Get current permissions
		statCmd := fmt.Sprintf("stat -c '%%a %%U %%G' %s 2>/dev/null", path)
		statResult, err := client.Exec(ctx, statCmd)
		if err == nil && statResult.ExitCode == 0 {
			parts := strings.Fields(statResult.Stdout)
			if len(parts) >= 3 {
				result.Actual.Mode = parts[0]
				result.Actual.Owner = parts[1]
				result.Actual.Group = parts[2]
			}
		}

		// Compare
		if currentHash != expected.Hash {
			result.Status = DriftStatusContentChanged
		} else if result.Actual.Mode != expected.Mode ||
			result.Actual.Owner != expected.Owner ||
			result.Actual.Group != expected.Group {
			result.Status = DriftStatusPermissionsChanged
		} else {
			result.Status = DriftStatusOK
		}

		results = append(results, result)
	}

	return results, nil
}

// DriftStatus represents the drift status of a file
type DriftStatus string

const (
	DriftStatusOK                 DriftStatus = "ok"
	DriftStatusMissing            DriftStatus = "missing"
	DriftStatusContentChanged     DriftStatus = "content_changed"
	DriftStatusPermissionsChanged DriftStatus = "permissions_changed"
)

// DriftResult represents the result of a drift check for a single file
type DriftResult struct {
	Path     string
	Status   DriftStatus
	Expected FileState
	Actual   FileState
}

// HasDrift returns true if there is any drift
func (r DriftResult) HasDrift() bool {
	return r.Status != DriftStatusOK
}

// FixDrift restores a file to its expected state
func (m *Manager) FixDrift(ctx context.Context, client *ssh.Client, drift DriftResult, content []byte) error {
	if drift.Status == DriftStatusOK {
		return nil
	}

	// Write content
	encoded := hashContent(content)
	_ = encoded // Would use base64 encoding for transfer

	// For now, we'll use the hash approach - actual content would come from the Nix store
	// This is a placeholder - real implementation would copy from store path

	// Fix permissions
	chmodCmd := fmt.Sprintf("chmod %s %s", drift.Expected.Mode, drift.Path)
	if _, err := client.ExecSudo(ctx, chmodCmd); err != nil {
		return fmt.Errorf("fixing mode: %w", err)
	}

	chownCmd := fmt.Sprintf("chown %s:%s %s", drift.Expected.Owner, drift.Expected.Group, drift.Path)
	if _, err := client.ExecSudo(ctx, chownCmd); err != nil {
		return fmt.Errorf("fixing ownership: %w", err)
	}

	return nil
}

// GatherOSInfo collects operating system information from a remote host
func (m *Manager) GatherOSInfo(ctx context.Context, client *ssh.Client) (*OSInfo, error) {
	info := &OSInfo{}

	// Parse /etc/os-release for distribution info
	osReleaseCmd := `cat /etc/os-release 2>/dev/null | grep -E '^(NAME|VERSION|VERSION_ID|PRETTY_NAME|VERSION_CODENAME)=' | sed 's/"//g'`
	result, err := client.Exec(ctx, osReleaseCmd)
	if err == nil && result.ExitCode == 0 {
		for _, line := range strings.Split(result.Stdout, "\n") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key, value := parts[0], parts[1]
			switch key {
			case "NAME":
				info.Name = value
			case "VERSION":
				info.Version = value
			case "VERSION_ID":
				info.VersionID = value
			case "PRETTY_NAME":
				info.PrettyName = value
			case "VERSION_CODENAME":
				info.Codename = value
			}
		}
	}

	// Get kernel version
	kernelResult, err := client.Exec(ctx, "uname -r")
	if err == nil && kernelResult.ExitCode == 0 {
		info.Kernel = strings.TrimSpace(kernelResult.Stdout)
	}

	// Get architecture
	archResult, err := client.Exec(ctx, "uname -m")
	if err == nil && archResult.ExitCode == 0 {
		info.Architecture = strings.TrimSpace(archResult.Stdout)
	}

	// Get uptime in human-readable format
	uptimeResult, err := client.Exec(ctx, "uptime -p 2>/dev/null || uptime | sed 's/.*up //' | sed 's/,.*load.*//'")
	if err == nil && uptimeResult.ExitCode == 0 {
		info.Uptime = strings.TrimSpace(uptimeResult.Stdout)
	}

	// Get last boot time
	bootResult, err := client.Exec(ctx, "who -b 2>/dev/null | awk '{print $3, $4}' || uptime -s 2>/dev/null")
	if err == nil && bootResult.ExitCode == 0 {
		info.LastBoot = strings.TrimSpace(bootResult.Stdout)
	}

	return info, nil
}

// UpdateOSInfo updates the OS information in state
func (m *Manager) UpdateOSInfo(ctx context.Context, client *ssh.Client) error {
	state, err := m.ReadState(ctx, client)
	if err != nil {
		state = NewHostState("", "")
	}

	osInfo, err := m.GatherOSInfo(ctx, client)
	if err != nil {
		return fmt.Errorf("gathering OS info: %w", err)
	}

	state.OSInfo = osInfo
	return m.WriteState(ctx, client, state)
}

// hashContent returns SHA256 hash of content
func hashContent(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// GetHostSummary returns a summary of the host state
func (s *HostState) GetHostSummary() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Host: %s (%s)\n", s.Hostname, s.Base))
	sb.WriteString(fmt.Sprintf("Generation: %d\n", s.CurrentGeneration))
	sb.WriteString(fmt.Sprintf("Last Apply: %s\n", s.LastApply.Format(time.RFC3339)))

	if s.RebootRequired {
		sb.WriteString("Reboot Required: YES\n")
		if len(s.RebootPackages) > 0 {
			sb.WriteString(fmt.Sprintf("  Packages: %s\n", strings.Join(s.RebootPackages, ", ")))
		}
	}

	if s.PendingUpdates > 0 {
		sb.WriteString(fmt.Sprintf("Pending Updates: %d (%d security)\n", s.PendingUpdates, s.SecurityUpdates))
	}

	if s.DriftDetected {
		sb.WriteString(fmt.Sprintf("Drift Detected: %d files\n", len(s.DriftFiles)))
	}

	// Service health summary
	healthy := 0
	unhealthy := 0
	for _, status := range s.ServiceHealth {
		if status.Active {
			healthy++
		} else {
			unhealthy++
		}
	}
	if len(s.ServiceHealth) > 0 {
		sb.WriteString(fmt.Sprintf("Services: %d healthy, %d unhealthy\n", healthy, unhealthy))
	}

	return sb.String()
}

// PlanDiff represents differences between desired and current state
type PlanDiff struct {
	FilesChanged     []FileDiff
	FilesAdded       []string
	FilesRemoved     []string
	UnitsToRestart   []string
	PackagesToAdd    []string
	PackagesToRemove []string
	UsersToAdd       []string
	UsersToRemove    []string
	RebootRequired   bool
}

// FileDiff represents a file change
type FileDiff struct {
	Path        string
	ChangeType  string // added, modified, removed
	ContentDiff string // unified diff if available
}

// HasChanges returns true if there are any changes
func (p *PlanDiff) HasChanges() bool {
	return len(p.FilesChanged) > 0 ||
		len(p.FilesAdded) > 0 ||
		len(p.FilesRemoved) > 0 ||
		len(p.UnitsToRestart) > 0 ||
		len(p.PackagesToAdd) > 0 ||
		len(p.PackagesToRemove) > 0 ||
		len(p.UsersToAdd) > 0 ||
		len(p.UsersToRemove) > 0
}

// Summary returns a human-readable summary
func (p *PlanDiff) Summary() string {
	var sb strings.Builder

	if !p.HasChanges() {
		sb.WriteString("No changes detected.\n")
		return sb.String()
	}

	if len(p.FilesAdded) > 0 {
		sb.WriteString(fmt.Sprintf("Files to add: %d\n", len(p.FilesAdded)))
		for _, f := range p.FilesAdded {
			sb.WriteString(fmt.Sprintf("  + %s\n", f))
		}
	}

	if len(p.FilesChanged) > 0 {
		sb.WriteString(fmt.Sprintf("Files to modify: %d\n", len(p.FilesChanged)))
		for _, f := range p.FilesChanged {
			sb.WriteString(fmt.Sprintf("  ~ %s\n", f.Path))
		}
	}

	if len(p.FilesRemoved) > 0 {
		sb.WriteString(fmt.Sprintf("Files to remove: %d\n", len(p.FilesRemoved)))
		for _, f := range p.FilesRemoved {
			sb.WriteString(fmt.Sprintf("  - %s\n", f))
		}
	}

	if len(p.PackagesToAdd) > 0 {
		sb.WriteString(fmt.Sprintf("Packages to add: %s\n", strings.Join(p.PackagesToAdd, ", ")))
	}

	if len(p.PackagesToRemove) > 0 {
		sb.WriteString(fmt.Sprintf("Packages to remove: %s\n", strings.Join(p.PackagesToRemove, ", ")))
	}

	if len(p.UnitsToRestart) > 0 {
		sb.WriteString(fmt.Sprintf("Units to restart: %s\n", strings.Join(p.UnitsToRestart, ", ")))
	}

	if p.RebootRequired {
		sb.WriteString("Reboot will be required.\n")
	}

	return sb.String()
}

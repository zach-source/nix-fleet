// Package nodestatus provides a lightweight HTTP server for reporting
// node status in pull-mode deployments.
package nodestatus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds the server configuration
type Config struct {
	Port         int
	BindAddress  string
	StateDir     string
	LogFile      string
	HostName     string
	ManifestHash string
	Version      string
	GitCommit    string
	GitTag       string
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	hostname, _ := os.Hostname()
	return Config{
		Port:        9100,
		BindAddress: "0.0.0.0",
		StateDir:    "/var/lib/nixfleet",
		LogFile:     "/var/log/nixfleet/pull.log",
		HostName:    hostname,
	}
}

// Status represents the overall node status
type Status struct {
	Hostname     string       `json:"hostname"`
	Status       string       `json:"status"` // healthy, degraded, unhealthy, unknown
	Timestamp    time.Time    `json:"timestamp"`
	Version      *VersionInfo `json:"version,omitempty"`
	ManifestHash string       `json:"manifestHash,omitempty"`
	PullMode     *PullStatus  `json:"pullMode,omitempty"`
	State        *StateInfo   `json:"state,omitempty"`
	Health       *HealthInfo  `json:"health,omitempty"`
	Uptime       string       `json:"uptime,omitempty"`
}

// VersionInfo represents the nixfleet version information
type VersionInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"gitCommit,omitempty"`
	GitTag    string `json:"gitTag,omitempty"`
}

// PullStatus represents the pull mode status
type PullStatus struct {
	LastRun       *time.Time `json:"lastRun,omitempty"`
	LastSuccess   *time.Time `json:"lastSuccess,omitempty"`
	LastFailure   *time.Time `json:"lastFailure,omitempty"`
	LastCommit    string     `json:"lastCommit,omitempty"`
	RecentEntries []string   `json:"recentEntries,omitempty"`
}

// StateInfo represents the state.json contents
type StateInfo struct {
	Generation   int       `json:"generation"`
	ManifestHash string    `json:"manifestHash"`
	LastApply    time.Time `json:"lastApply"`
}

// HealthInfo represents health check status
type HealthInfo struct {
	Checks  map[string]string `json:"checks,omitempty"`
	Summary string            `json:"summary"` // all_passing, some_failing, all_failing
}

// Server is the node status HTTP server
type Server struct {
	config Config
	server *http.Server
}

// NewServer creates a new node status server
func NewServer(cfg Config) *Server {
	return &Server{config: cfg}
}

// Start starts the HTTP server
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Status endpoints
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/pull", s.handlePullStatus)
	mux.HandleFunc("/state", s.handleState)

	addr := fmt.Sprintf("%s:%d", s.config.BindAddress, s.config.Port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start server in goroutine
	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("Node status server listening on %s\n", addr)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>NixFleet Node Status - %s</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 2rem; }
h1 { color: #333; }
.endpoint { margin: 0.5rem 0; }
a { color: #0066cc; }
</style>
</head>
<body>
<h1>NixFleet Node Status</h1>
<p>Hostname: <strong>%s</strong></p>
<h2>Endpoints</h2>
<div class="endpoint"><a href="/status">/status</a> - Full node status (JSON)</div>
<div class="endpoint"><a href="/health">/health</a> - Health check endpoint</div>
<div class="endpoint"><a href="/pull">/pull</a> - Pull mode status</div>
<div class="endpoint"><a href="/state">/state</a> - State information</div>
</body>
</html>`, s.config.HostName, s.config.HostName)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.gatherStatus()

	w.Header().Set("Content-Type", "application/json")
	if status.Status == "unhealthy" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := s.gatherStatus()

	w.Header().Set("Content-Type", "application/json")

	response := struct {
		Status   string       `json:"status"`
		Hostname string       `json:"hostname"`
		Time     time.Time    `json:"time"`
		Version  *VersionInfo `json:"version,omitempty"`
	}{
		Status:   status.Status,
		Hostname: s.config.HostName,
		Time:     time.Now(),
		Version:  status.Version,
	}

	if status.Status == "unhealthy" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handlePullStatus(w http.ResponseWriter, r *http.Request) {
	pullStatus := s.gatherPullStatus()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pullStatus)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	stateInfo := s.gatherStateInfo()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stateInfo)
}

func (s *Server) gatherStatus() Status {
	status := Status{
		Hostname:  s.config.HostName,
		Timestamp: time.Now(),
		Status:    "healthy",
	}

	// Add version info
	if s.config.Version != "" {
		status.Version = &VersionInfo{
			Version:   s.config.Version,
			GitCommit: s.config.GitCommit,
			GitTag:    s.config.GitTag,
		}
	}

	// Gather state info
	stateInfo := s.gatherStateInfo()
	if stateInfo != nil {
		status.State = stateInfo
		status.ManifestHash = stateInfo.ManifestHash
	}

	// Gather pull status
	pullStatus := s.gatherPullStatus()
	if pullStatus != nil {
		status.PullMode = pullStatus
	}

	// Gather health info
	healthInfo := s.gatherHealthInfo()
	if healthInfo != nil {
		status.Health = healthInfo
		if healthInfo.Summary == "all_failing" {
			status.Status = "unhealthy"
		} else if healthInfo.Summary == "some_failing" {
			status.Status = "degraded"
		}
	}

	// Get uptime
	if uptime, err := s.getUptime(); err == nil {
		status.Uptime = uptime
	}

	// Check for recent failures in pull mode
	if pullStatus != nil && pullStatus.LastFailure != nil {
		if pullStatus.LastSuccess == nil || pullStatus.LastFailure.After(*pullStatus.LastSuccess) {
			if status.Status == "healthy" {
				status.Status = "degraded"
			}
		}
	}

	return status
}

func (s *Server) gatherStateInfo() *StateInfo {
	statePath := filepath.Join(s.config.StateDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}

	var raw struct {
		Generation   int    `json:"generation"`
		ManifestHash string `json:"manifestHash"`
		LastApply    string `json:"lastApply"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	info := &StateInfo{
		Generation:   raw.Generation,
		ManifestHash: raw.ManifestHash,
	}

	if t, err := time.Parse(time.RFC3339, raw.LastApply); err == nil {
		info.LastApply = t
	}

	return info
}

func (s *Server) gatherPullStatus() *PullStatus {
	if s.config.LogFile == "" {
		return nil
	}

	file, err := os.Open(s.config.LogFile)
	if err != nil {
		return nil
	}
	defer file.Close()

	status := &PullStatus{}

	// Read last N lines of log file
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > 100 {
			lines = lines[1:]
		}
	}

	// Parse log entries
	for _, line := range lines {
		// Extract timestamp from log line (format: 2025-01-01T12:00:00+00:00 message)
		if len(line) > 25 && line[4] == '-' && line[10] == 'T' {
			tsStr := line[:25]
			if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
				status.LastRun = &t

				msg := line[26:]
				if strings.Contains(msg, "Pull operation completed successfully") ||
					strings.Contains(msg, "Successfully applied") ||
					strings.Contains(msg, "No changes detected") {
					status.LastSuccess = &t
				}
				if strings.Contains(msg, "ERROR") || strings.Contains(msg, "failed") {
					status.LastFailure = &t
				}
				if strings.Contains(msg, "commit") && strings.Contains(msg, ":") {
					// Extract commit hash
					parts := strings.Split(msg, " ")
					for _, p := range parts {
						if len(p) >= 7 && len(p) <= 40 && isHex(p) {
							status.LastCommit = p
							break
						}
					}
				}
			}
		}
	}

	// Keep last 10 log entries for display
	if len(lines) > 10 {
		status.RecentEntries = lines[len(lines)-10:]
	} else {
		status.RecentEntries = lines
	}

	return status
}

func (s *Server) gatherHealthInfo() *HealthInfo {
	healthPath := filepath.Join(s.config.StateDir, "health.json")
	data, err := os.ReadFile(healthPath)
	if err != nil {
		// Try reading from system profile
		profileHealth := "/nix/var/nix/profiles/nixfleet/system/health-checks.json"
		data, err = os.ReadFile(profileHealth)
		if err != nil {
			return nil
		}
	}

	var checks map[string]interface{}
	if err := json.Unmarshal(data, &checks); err != nil {
		return nil
	}

	if len(checks) == 0 {
		return nil
	}

	info := &HealthInfo{
		Checks:  make(map[string]string),
		Summary: "all_passing",
	}

	// We just report what health checks are defined, not their status
	// (actual health check execution is done by the activate script)
	for name := range checks {
		info.Checks[name] = "defined"
	}

	return info
}

func (s *Server) getUptime() (string, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", err
	}

	var uptime float64
	if _, err := fmt.Sscanf(string(data), "%f", &uptime); err != nil {
		return "", err
	}

	d := time.Duration(uptime) * time.Second
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes), nil
	} else if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes), nil
	}
	return fmt.Sprintf("%dm", minutes), nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

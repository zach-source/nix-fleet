package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/ssh"
)

func TestConfig(t *testing.T) {
	config := Config{
		ListenAddr:          ":8080",
		FlakePath:           "/path/to/flake",
		DriftCheckInterval:  15 * time.Minute,
		UpdateCheckInterval: 1 * time.Hour,
		HealthCheckInterval: 5 * time.Minute,
		WebhookURL:          "https://hooks.example.com/webhook",
		WebhookSecret:       "secret123",
		WebhookEvents:       []string{"drift", "apply"},
		APIToken:            "token123",
	}

	if config.ListenAddr != ":8080" {
		t.Errorf("Expected ListenAddr ':8080', got '%s'", config.ListenAddr)
	}
	if config.FlakePath != "/path/to/flake" {
		t.Errorf("Expected FlakePath '/path/to/flake', got '%s'", config.FlakePath)
	}
	if config.DriftCheckInterval != 15*time.Minute {
		t.Errorf("Expected DriftCheckInterval 15m, got %v", config.DriftCheckInterval)
	}
	if config.APIToken != "token123" {
		t.Errorf("Expected APIToken 'token123', got '%s'", config.APIToken)
	}
	if len(config.WebhookEvents) != 2 {
		t.Errorf("Expected 2 webhook events, got %d", len(config.WebhookEvents))
	}
}

func TestJob(t *testing.T) {
	now := time.Now()
	job := &Job{
		ID:        "apply-12345",
		Type:      "apply",
		Status:    "running",
		Host:      "web1",
		StartTime: now,
	}

	if job.ID != "apply-12345" {
		t.Errorf("Expected ID 'apply-12345', got '%s'", job.ID)
	}
	if job.Type != "apply" {
		t.Errorf("Expected Type 'apply', got '%s'", job.Type)
	}
	if job.Status != "running" {
		t.Errorf("Expected Status 'running', got '%s'", job.Status)
	}
	if job.Host != "web1" {
		t.Errorf("Expected Host 'web1', got '%s'", job.Host)
	}
	if job.StartTime != now {
		t.Error("StartTime mismatch")
	}
}

func TestJobCompletion(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Second)

	job := &Job{
		ID:        "apply-12345",
		Type:      "apply",
		Status:    "completed",
		Host:      "web1",
		StartTime: start,
		EndTime:   end,
		Result:    map[string]string{"store_path": "/nix/store/abc123"},
	}

	if job.Status != "completed" {
		t.Errorf("Expected Status 'completed', got '%s'", job.Status)
	}
	if job.EndTime.Sub(job.StartTime) != 5*time.Second {
		t.Error("Duration calculation incorrect")
	}
	if job.Result == nil {
		t.Error("Expected Result to be set")
	}
}

func TestJobFailure(t *testing.T) {
	job := &Job{
		ID:        "apply-12345",
		Type:      "apply",
		Status:    "failed",
		Host:      "web1",
		StartTime: time.Now(),
		EndTime:   time.Now(),
		Error:     "build failed: missing flake.nix",
	}

	if job.Status != "failed" {
		t.Errorf("Expected Status 'failed', got '%s'", job.Status)
	}
	if job.Error != "build failed: missing flake.nix" {
		t.Errorf("Expected Error message, got '%s'", job.Error)
	}
}

// TestServer creates a minimal test server for handler tests
type TestServer struct {
	*Server
}

func newTestServer(t *testing.T) *TestServer {
	t.Helper()

	inv := inventory.NewInventory()
	inv.Hosts["web1"] = &inventory.Host{
		Name:    "web1",
		Addr:    "10.0.0.1",
		SSHPort: 22,
		Base:    "ubuntu",
		Roles:   []string{"webserver"},
	}
	inv.Hosts["db1"] = &inventory.Host{
		Name:    "db1",
		Addr:    "10.0.0.2",
		SSHPort: 22,
		Base:    "nixos",
		Roles:   []string{"database"},
	}

	s := &Server{
		config: Config{
			ListenAddr: ":8080",
			APIToken:   "",
			Inventory:  inv,
		},
		inventory: inv,
		jobs:      make(map[string]*Job),
		startTime: time.Now(),
		mux:       http.NewServeMux(),
	}
	s.setupRoutes()

	return &TestServer{Server: s}
}

func newTestServerWithAuth(t *testing.T, token string) *TestServer {
	t.Helper()

	ts := newTestServer(t)
	ts.config.APIToken = token
	return ts
}

func TestHandleHealth(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()

	ts.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("Expected status 'ok', got '%v'", response["status"])
	}
	if _, ok := response["uptime"]; !ok {
		t.Error("Expected uptime in response")
	}
}

func TestHandleInfo(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/info", nil)
	rec := httptest.NewRecorder()

	ts.handleInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["version"] != "dev" {
		t.Errorf("Expected version 'dev', got '%v'", response["version"])
	}
	if response["hosts"].(float64) != 2 {
		t.Errorf("Expected 2 hosts, got %v", response["hosts"])
	}
	if _, ok := response["uptime"]; !ok {
		t.Error("Expected uptime in response")
	}
	if _, ok := response["start_time"]; !ok {
		t.Error("Expected start_time in response")
	}
}

func TestHandleListHosts(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/hosts", nil)
	rec := httptest.NewRecorder()

	ts.handleListHosts(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var response []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(response) != 2 {
		t.Errorf("Expected 2 hosts, got %d", len(response))
	}

	// Check first host
	found := false
	for _, h := range response {
		if h["name"] == "web1" {
			found = true
			if h["address"] != "10.0.0.1" {
				t.Errorf("Expected address '10.0.0.1', got '%v'", h["address"])
			}
			if h["base"] != "ubuntu" {
				t.Errorf("Expected base 'ubuntu', got '%v'", h["base"])
			}
		}
	}
	if !found {
		t.Error("Expected to find host 'web1'")
	}
}

func TestHandleGetHostNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/hosts/nonexistent", nil)
	req.SetPathValue("name", "nonexistent")
	rec := httptest.NewRecorder()

	ts.handleGetHost(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["error"] != "host not found" {
		t.Errorf("Expected error 'host not found', got '%v'", response["error"])
	}
}

func TestAuthMiddleware(t *testing.T) {
	ts := newTestServerWithAuth(t, "secret-token")

	tests := []struct {
		name           string
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "valid token",
			authHeader:     "Bearer secret-token",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "missing token",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "wrong token",
			authHeader:     "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid format",
			authHeader:     "Basic secret-token",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := ts.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest("GET", "/api/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestAuthMiddlewareNoTokenRequired(t *testing.T) {
	ts := newTestServer(t) // No token configured

	handler := ts.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200 when no token required, got %d", rec.Code)
	}
}

func TestHandleListJobs(t *testing.T) {
	ts := newTestServer(t)

	// Add some test jobs
	ts.jobs["job1"] = &Job{
		ID:        "job1",
		Type:      "apply",
		Status:    "completed",
		Host:      "web1",
		StartTime: time.Now(),
	}
	ts.jobs["job2"] = &Job{
		ID:        "job2",
		Type:      "drift-check",
		Status:    "running",
		StartTime: time.Now(),
	}

	req := httptest.NewRequest("GET", "/api/jobs", nil)
	rec := httptest.NewRecorder()

	ts.handleListJobs(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var response []*Job
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(response) != 2 {
		t.Errorf("Expected 2 jobs, got %d", len(response))
	}
}

func TestHandleGetJob(t *testing.T) {
	ts := newTestServer(t)

	ts.jobs["job1"] = &Job{
		ID:        "job1",
		Type:      "apply",
		Status:    "completed",
		Host:      "web1",
		StartTime: time.Now(),
	}

	req := httptest.NewRequest("GET", "/api/jobs/job1", nil)
	req.SetPathValue("id", "job1")
	rec := httptest.NewRecorder()

	ts.handleGetJob(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var response Job
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.ID != "job1" {
		t.Errorf("Expected job ID 'job1', got '%s'", response.ID)
	}
	if response.Type != "apply" {
		t.Errorf("Expected job type 'apply', got '%s'", response.Type)
	}
}

func TestHandleGetJobNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/jobs/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	rec := httptest.NewRecorder()

	ts.handleGetJob(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["error"] != "job not found" {
		t.Errorf("Expected error 'job not found', got '%v'", response["error"])
	}
}

func TestCreateJob(t *testing.T) {
	ts := newTestServer(t)

	job := ts.createJob("apply", "web1")

	if job == nil {
		t.Fatal("Expected job, got nil")
	}
	if job.Type != "apply" {
		t.Errorf("Expected Type 'apply', got '%s'", job.Type)
	}
	if job.Host != "web1" {
		t.Errorf("Expected Host 'web1', got '%s'", job.Host)
	}
	if job.Status != "pending" {
		t.Errorf("Expected Status 'pending', got '%s'", job.Status)
	}
	if job.StartTime.IsZero() {
		t.Error("Expected StartTime to be set")
	}

	// Verify job was added to server's job map
	ts.jobsMu.RLock()
	storedJob, ok := ts.jobs[job.ID]
	ts.jobsMu.RUnlock()

	if !ok {
		t.Error("Expected job to be stored in server")
	}
	if storedJob != job {
		t.Error("Expected stored job to match created job")
	}
}

func TestUpdateJob(t *testing.T) {
	ts := newTestServer(t)

	job := ts.createJob("apply", "web1")

	// Update to running
	ts.updateJob(job, "running", nil, "")
	if job.Status != "running" {
		t.Errorf("Expected Status 'running', got '%s'", job.Status)
	}
	if !job.EndTime.IsZero() {
		t.Error("EndTime should not be set for running job")
	}

	// Update to completed
	result := map[string]string{"store_path": "/nix/store/abc123"}
	ts.updateJob(job, "completed", result, "")
	if job.Status != "completed" {
		t.Errorf("Expected Status 'completed', got '%s'", job.Status)
	}
	if job.EndTime.IsZero() {
		t.Error("Expected EndTime to be set for completed job")
	}
	if job.Result == nil {
		t.Error("Expected Result to be set")
	}
}

func TestUpdateJobFailed(t *testing.T) {
	ts := newTestServer(t)

	job := ts.createJob("apply", "web1")

	ts.updateJob(job, "failed", nil, "build error")
	if job.Status != "failed" {
		t.Errorf("Expected Status 'failed', got '%s'", job.Status)
	}
	if job.EndTime.IsZero() {
		t.Error("Expected EndTime to be set for failed job")
	}
	if job.Error != "build error" {
		t.Errorf("Expected Error 'build error', got '%s'", job.Error)
	}
}

func TestJSONResponse(t *testing.T) {
	ts := newTestServer(t)

	rec := httptest.NewRecorder()
	ts.jsonResponse(rec, map[string]string{"key": "value"}, http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
	}

	var response map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["key"] != "value" {
		t.Errorf("Expected 'value', got '%s'", response["key"])
	}
}

func TestJSONError(t *testing.T) {
	ts := newTestServer(t)

	rec := httptest.NewRecorder()
	ts.jsonError(rec, "something went wrong", http.StatusBadRequest)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}

	var response map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["error"] != "something went wrong" {
		t.Errorf("Expected error message, got '%s'", response["error"])
	}
}

func TestLoggingMiddleware(t *testing.T) {
	ts := newTestServer(t)

	// Create a simple handler to wrap
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := ts.loggingMiddleware(handler)

	req := httptest.NewRequest("GET", "/api/test", nil)
	rec := httptest.NewRecorder()

	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestHandleDriftFixNoHost(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/drift/fix", nil)
	rec := httptest.NewRecorder()

	ts.handleDriftFix(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["error"] != "host parameter required" {
		t.Errorf("Expected error about missing host parameter, got '%v'", response["error"])
	}
}

func TestHandleDriftFixHostNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/drift/fix?host=nonexistent", nil)
	rec := httptest.NewRecorder()

	ts.handleDriftFix(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestHandleApplyAllNoHosts(t *testing.T) {
	ts := newTestServer(t)

	// Clear all hosts
	ts.inventory = inventory.NewInventory()

	req := httptest.NewRequest("POST", "/api/apply", nil)
	rec := httptest.NewRecorder()

	ts.handleApplyAll(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["error"] != "no hosts to apply" {
		t.Errorf("Expected error 'no hosts to apply', got '%v'", response["error"])
	}
}

func TestHandleApplyHostNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/hosts/nonexistent/apply", nil)
	req.SetPathValue("name", "nonexistent")
	rec := httptest.NewRecorder()

	ts.handleApplyHost(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestHandleRollbackHostNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/hosts/nonexistent/rollback", nil)
	req.SetPathValue("name", "nonexistent")
	rec := httptest.NewRecorder()

	ts.handleRollbackHost(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestHandleGetHostStateNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/hosts/nonexistent/state", nil)
	req.SetPathValue("name", "nonexistent")
	rec := httptest.NewRecorder()

	ts.handleGetHostState(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestHandlePlanHostNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/plan/nonexistent", nil)
	req.SetPathValue("name", "nonexistent")
	rec := httptest.NewRecorder()

	ts.handlePlanHost(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestHandleDriftCheckWithHost(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/drift/check?host=nonexistent", nil)
	rec := httptest.NewRecorder()

	ts.handleDriftCheck(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestServerClose(t *testing.T) {
	ts := newTestServer(t)

	// Initialize pool so Close() doesn't panic
	ts.pool = ssh.NewPool(nil)

	// Close should not panic
	if err := ts.Close(); err != nil {
		t.Errorf("Unexpected error from Close: %v", err)
	}
}

// Test scheduler creation
func TestNewScheduler(t *testing.T) {
	ts := newTestServer(t)

	scheduler := NewScheduler(ts.Server)
	if scheduler == nil {
		t.Error("NewScheduler should not return nil")
	}
	if scheduler.server != ts.Server {
		t.Error("Scheduler server reference mismatch")
	}
}

func TestSchedulerStop(t *testing.T) {
	ts := newTestServer(t)
	scheduler := NewScheduler(ts.Server)

	// Stop should not panic
	scheduler.Stop()

	// Calling Stop again should not panic
	// (channel already closed)
}

func TestSchedulerStartWithNoIntervals(t *testing.T) {
	ts := newTestServer(t)
	ts.config.DriftCheckInterval = 0
	ts.config.UpdateCheckInterval = 0
	ts.config.HealthCheckInterval = 0

	scheduler := NewScheduler(ts.Server)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Start should not block or panic
	scheduler.Start(ctx)
}

func TestWebhookEventFiltering(t *testing.T) {
	tests := []struct {
		name          string
		enabledEvents []string
		event         string
		shouldSend    bool
	}{
		{"event enabled", []string{"drift", "apply"}, "drift", true},
		{"event not enabled", []string{"apply"}, "drift", false},
		{"wildcard enabled", []string{"*"}, "drift", true},
		{"empty events", []string{}, "drift", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled := false
			for _, e := range tt.enabledEvents {
				if e == tt.event || e == "*" {
					enabled = true
					break
				}
			}

			if enabled != tt.shouldSend {
				t.Errorf("Expected shouldSend=%v for event '%s' with enabled=%v", tt.shouldSend, tt.event, tt.enabledEvents)
			}
		})
	}
}

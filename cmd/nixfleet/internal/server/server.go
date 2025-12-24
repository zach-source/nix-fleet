// Package server implements the NixFleet HTTP API server
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
)

// Config holds server configuration
type Config struct {
	ListenAddr string
	FlakePath  string
	Inventory  *inventory.Inventory

	// Scheduler settings
	DriftCheckInterval  time.Duration
	UpdateCheckInterval time.Duration
	HealthCheckInterval time.Duration

	// Webhook settings
	WebhookURL    string
	WebhookSecret string
	WebhookEvents []string // drift, apply, reboot, health

	// Auth settings
	APIToken string
}

// Server is the NixFleet HTTP API server
type Server struct {
	config    Config
	inventory *inventory.Inventory
	evaluator *nix.Evaluator
	deployer  *nix.Deployer
	pool      *ssh.Pool
	stateMgr  *state.Manager

	// Scheduler
	scheduler *Scheduler

	// Job tracking
	jobs   map[string]*Job
	jobsMu sync.RWMutex

	// Server state
	startTime time.Time
	mux       *http.ServeMux
}

// Job represents an async operation
type Job struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`   // apply, drift-check, update-check
	Status    string    `json:"status"` // pending, running, completed, failed
	Host      string    `json:"host,omitempty"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time,omitempty"`
	Result    any       `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// New creates a new server instance
func New(config Config) (*Server, error) {
	flake, err := nix.ResolveFlakePath(config.FlakePath)
	if err != nil {
		return nil, fmt.Errorf("resolving flake path: %w", err)
	}

	evaluator, err := nix.NewEvaluator(flake)
	if err != nil {
		return nil, fmt.Errorf("creating evaluator: %w", err)
	}

	s := &Server{
		config:    config,
		inventory: config.Inventory,
		evaluator: evaluator,
		deployer:  nix.NewDeployer(evaluator),
		pool:      ssh.NewPool(nil),
		stateMgr:  state.NewManager(),
		jobs:      make(map[string]*Job),
		startTime: time.Now(),
		mux:       http.NewServeMux(),
	}

	s.setupRoutes()
	s.scheduler = NewScheduler(s)

	return s, nil
}

// setupRoutes configures HTTP handlers
func (s *Server) setupRoutes() {
	// Health and info
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/info", s.handleInfo)

	// Hosts
	s.mux.HandleFunc("GET /api/hosts", s.authMiddleware(s.handleListHosts))
	s.mux.HandleFunc("GET /api/hosts/{name}", s.authMiddleware(s.handleGetHost))
	s.mux.HandleFunc("GET /api/hosts/{name}/state", s.authMiddleware(s.handleGetHostState))
	s.mux.HandleFunc("POST /api/hosts/{name}/apply", s.authMiddleware(s.handleApplyHost))
	s.mux.HandleFunc("POST /api/hosts/{name}/rollback", s.authMiddleware(s.handleRollbackHost))

	// Drift
	s.mux.HandleFunc("GET /api/drift", s.authMiddleware(s.handleDriftStatus))
	s.mux.HandleFunc("POST /api/drift/check", s.authMiddleware(s.handleDriftCheck))
	s.mux.HandleFunc("POST /api/drift/fix", s.authMiddleware(s.handleDriftFix))

	// Jobs
	s.mux.HandleFunc("GET /api/jobs", s.authMiddleware(s.handleListJobs))
	s.mux.HandleFunc("GET /api/jobs/{id}", s.authMiddleware(s.handleGetJob))

	// Plan
	s.mux.HandleFunc("GET /api/plan", s.authMiddleware(s.handlePlan))
	s.mux.HandleFunc("GET /api/plan/{name}", s.authMiddleware(s.handlePlanHost))

	// Apply (fleet-wide)
	s.mux.HandleFunc("POST /api/apply", s.authMiddleware(s.handleApplyAll))
}

// authMiddleware wraps handlers with token authentication
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.config.APIToken != "" {
			auth := r.Header.Get("Authorization")
			expected := "Bearer " + s.config.APIToken
			if auth != expected {
				s.jsonError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

// Start starts the HTTP server
func (s *Server) Start(ctx context.Context) error {
	// Start scheduler
	s.scheduler.Start(ctx)

	server := &http.Server{
		Addr:         s.config.ListenAddr,
		Handler:      s.loggingMiddleware(s.mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Starting NixFleet server on %s", s.config.ListenAddr)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Println("Shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// loggingMiddleware logs requests
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// Close cleans up resources
func (s *Server) Close() error {
	s.pool.Close()
	return nil
}

// JSON response helpers
func (s *Server) jsonResponse(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (s *Server) jsonError(w http.ResponseWriter, message string, status int) {
	s.jsonResponse(w, map[string]string{"error": message}, status)
}

// Handler implementations

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, map[string]any{
		"status": "ok",
		"uptime": time.Since(s.startTime).String(),
	}, http.StatusOK)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, map[string]any{
		"version":    "dev",
		"start_time": s.startTime,
		"uptime":     time.Since(s.startTime).String(),
		"hosts":      len(s.inventory.AllHosts()),
		"flake_path": s.config.FlakePath,
	}, http.StatusOK)
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts := s.inventory.AllHosts()
	result := make([]map[string]any, 0, len(hosts))

	for _, h := range hosts {
		result = append(result, map[string]any{
			"name":    h.Name,
			"address": h.Addr,
			"port":    h.SSHPort,
			"base":    h.Base,
			"roles":   h.Roles,
		})
	}

	s.jsonResponse(w, result, http.StatusOK)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	host, ok := s.inventory.GetHost(name)
	if !ok {
		s.jsonError(w, "host not found", http.StatusNotFound)
		return
	}

	ctx := r.Context()

	// Get connection and state
	client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
	if err != nil {
		s.jsonResponse(w, map[string]any{
			"name":    host.Name,
			"address": host.Addr,
			"port":    host.SSHPort,
			"base":    host.Base,
			"groups":  host.Roles,
			"online":  false,
			"error":   err.Error(),
		}, http.StatusOK)
		return
	}

	// Get state
	hostState, _ := s.stateMgr.ReadState(ctx, client)

	// Get current generation
	gen, storePath, _ := s.deployer.GetCurrentGeneration(ctx, client, host.Base)
	reboot, _ := s.deployer.CheckRebootNeeded(ctx, client, host.Base)

	result := map[string]any{
		"name":       host.Name,
		"address":    host.Addr,
		"port":       host.SSHPort,
		"base":       host.Base,
		"groups":     host.Roles,
		"online":     true,
		"generation": gen,
		"store_path": storePath,
		"reboot":     reboot,
	}

	if hostState != nil {
		result["state"] = hostState
	}

	s.jsonResponse(w, result, http.StatusOK)
}

func (s *Server) handleGetHostState(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	host, ok := s.inventory.GetHost(name)
	if !ok {
		s.jsonError(w, "host not found", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
	if err != nil {
		s.jsonError(w, "connection failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	hostState, err := s.stateMgr.ReadState(ctx, client)
	if err != nil {
		s.jsonError(w, "failed to read state: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, hostState, http.StatusOK)
}

func (s *Server) handleApplyHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	host, ok := s.inventory.GetHost(name)
	if !ok {
		s.jsonError(w, "host not found", http.StatusNotFound)
		return
	}

	// Create async job
	job := s.createJob("apply", name)

	go func() {
		ctx := context.Background()
		s.runApplyJob(ctx, job, host)
	}()

	s.jsonResponse(w, job, http.StatusAccepted)
}

func (s *Server) handleRollbackHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	host, ok := s.inventory.GetHost(name)
	if !ok {
		s.jsonError(w, "host not found", http.StatusNotFound)
		return
	}

	// Parse generation from query
	genStr := r.URL.Query().Get("generation")
	generation := 0
	if genStr != "" && genStr != "previous" {
		fmt.Sscanf(genStr, "%d", &generation)
	}

	ctx := r.Context()
	client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
	if err != nil {
		s.jsonError(w, "connection failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := s.deployer.Rollback(ctx, client, host.Base, generation); err != nil {
		s.jsonError(w, "rollback failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, map[string]string{"status": "rolled back"}, http.StatusOK)
}

func (s *Server) handleDriftStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hosts := s.inventory.AllHosts()

	// Filter by group if specified
	group := r.URL.Query().Get("group")
	if group != "" {
		hosts = s.inventory.HostsInGroup(group)
	}

	results := make([]map[string]any, 0)

	for _, host := range hosts {
		client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
		if err != nil {
			results = append(results, map[string]any{
				"host":   host.Name,
				"online": false,
				"error":  err.Error(),
			})
			continue
		}

		hostState, _ := s.stateMgr.ReadState(ctx, client)

		result := map[string]any{
			"host":   host.Name,
			"online": true,
		}

		if hostState != nil {
			result["drift_detected"] = hostState.DriftDetected
			result["drift_files"] = hostState.DriftFiles
			result["last_check"] = hostState.LastDriftCheck
		}

		results = append(results, result)
	}

	s.jsonResponse(w, results, http.StatusOK)
}

func (s *Server) handleDriftCheck(w http.ResponseWriter, r *http.Request) {
	// Get target hosts
	hostName := r.URL.Query().Get("host")
	group := r.URL.Query().Get("group")

	var hosts []*inventory.Host
	if hostName != "" {
		h, ok := s.inventory.GetHost(hostName)
		if !ok {
			s.jsonError(w, "host not found", http.StatusNotFound)
			return
		}
		hosts = []*inventory.Host{h}
	} else if group != "" {
		hosts = s.inventory.HostsInGroup(group)
	} else {
		hosts = s.inventory.AllHosts()
	}

	// Create job
	job := s.createJob("drift-check", "")

	go func() {
		ctx := context.Background()
		s.runDriftCheckJob(ctx, job, hosts)
	}()

	s.jsonResponse(w, job, http.StatusAccepted)
}

func (s *Server) handleDriftFix(w http.ResponseWriter, r *http.Request) {
	hostName := r.URL.Query().Get("host")
	if hostName == "" {
		s.jsonError(w, "host parameter required", http.StatusBadRequest)
		return
	}

	host, ok := s.inventory.GetHost(hostName)
	if !ok {
		s.jsonError(w, "host not found", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
	if err != nil {
		s.jsonError(w, "connection failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	hostState, err := s.stateMgr.ReadState(ctx, client)
	if err != nil {
		s.jsonError(w, "failed to read state: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if len(hostState.ManagedFiles) == 0 {
		s.jsonResponse(w, map[string]string{"status": "no managed files"}, http.StatusOK)
		return
	}

	results, err := s.stateMgr.CheckDrift(ctx, client, hostState.ManagedFiles)
	if err != nil {
		s.jsonError(w, "drift check failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fixed := 0
	for _, result := range results {
		if result.Status == state.DriftStatusPermissionsChanged {
			if err := s.stateMgr.FixDrift(ctx, client, result, nil); err == nil {
				fixed++
			}
		}
	}

	s.jsonResponse(w, map[string]any{
		"fixed":   fixed,
		"checked": len(results),
	}, http.StatusOK)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()

	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}

	s.jsonResponse(w, jobs, http.StatusOK)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.jobsMu.RLock()
	job, ok := s.jobs[id]
	s.jobsMu.RUnlock()

	if !ok {
		s.jsonError(w, "job not found", http.StatusNotFound)
		return
	}

	s.jsonResponse(w, job, http.StatusOK)
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hosts := s.inventory.AllHosts()

	group := r.URL.Query().Get("group")
	if group != "" {
		hosts = s.inventory.HostsInGroup(group)
	}

	results := make([]map[string]any, 0)

	for _, host := range hosts {
		result := map[string]any{
			"host": host.Name,
			"base": host.Base,
		}

		closure, err := s.evaluator.BuildHost(ctx, host.Name, host.Base)
		if err != nil {
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}

		result["store_path"] = closure.StorePath
		result["manifest_hash"] = closure.ManifestHash

		// Compare with current state
		client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
		if err != nil {
			result["status"] = "unreachable"
			result["connection_error"] = err.Error()
		} else {
			hostState, _ := s.stateMgr.ReadState(ctx, client)
			if hostState != nil && hostState.ManifestHash != "" {
				if hostState.ManifestHash == closure.ManifestHash {
					result["status"] = "up_to_date"
				} else {
					result["status"] = "changes_pending"
					result["current_hash"] = hostState.ManifestHash
				}
			} else {
				result["status"] = "new_deployment"
			}
		}

		results = append(results, result)
	}

	s.jsonResponse(w, results, http.StatusOK)
}

func (s *Server) handlePlanHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	host, ok := s.inventory.GetHost(name)
	if !ok {
		s.jsonError(w, "host not found", http.StatusNotFound)
		return
	}

	ctx := r.Context()

	closure, err := s.evaluator.BuildHost(ctx, host.Name, host.Base)
	if err != nil {
		s.jsonError(w, "build failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	size, _ := s.evaluator.GetClosureSize(ctx, closure.StorePath)

	result := map[string]any{
		"host":          host.Name,
		"store_path":    closure.StorePath,
		"manifest_hash": closure.ManifestHash,
		"closure_size":  size,
	}

	client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
	if err != nil {
		result["status"] = "unreachable"
	} else {
		hostState, _ := s.stateMgr.ReadState(ctx, client)
		if hostState != nil && hostState.ManifestHash != "" {
			if hostState.ManifestHash == closure.ManifestHash {
				result["status"] = "up_to_date"
			} else {
				result["status"] = "changes_pending"
				result["current_hash"] = hostState.ManifestHash
				result["current_path"] = hostState.StorePath
			}
		} else {
			result["status"] = "new_deployment"
		}
	}

	s.jsonResponse(w, result, http.StatusOK)
}

func (s *Server) handleApplyAll(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")

	var hosts []*inventory.Host
	if group != "" {
		hosts = s.inventory.HostsInGroup(group)
	} else {
		hosts = s.inventory.AllHosts()
	}

	if len(hosts) == 0 {
		s.jsonError(w, "no hosts to apply", http.StatusBadRequest)
		return
	}

	job := s.createJob("apply-all", "")

	go func() {
		ctx := context.Background()
		s.runApplyAllJob(ctx, job, hosts)
	}()

	s.jsonResponse(w, job, http.StatusAccepted)
}

// Job management

func (s *Server) createJob(jobType, host string) *Job {
	id := fmt.Sprintf("%s-%d", jobType, time.Now().UnixNano())
	job := &Job{
		ID:        id,
		Type:      jobType,
		Status:    "pending",
		Host:      host,
		StartTime: time.Now(),
	}

	s.jobsMu.Lock()
	s.jobs[id] = job
	s.jobsMu.Unlock()

	return job
}

func (s *Server) updateJob(job *Job, status string, result any, errStr string) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	job.Status = status
	job.Result = result
	job.Error = errStr
	if status == "completed" || status == "failed" {
		job.EndTime = time.Now()
	}
}

// Job runners

func (s *Server) runApplyJob(ctx context.Context, job *Job, host *inventory.Host) {
	s.updateJob(job, "running", nil, "")

	startTime := time.Now()

	// Build
	closure, err := s.evaluator.BuildHost(ctx, host.Name, host.Base)
	if err != nil {
		s.updateJob(job, "failed", nil, "build failed: "+err.Error())
		return
	}

	// Copy
	if err := s.deployer.CopyToHost(ctx, closure, host); err != nil {
		s.updateJob(job, "failed", nil, "copy failed: "+err.Error())
		return
	}

	// Activate
	client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
	if err != nil {
		s.updateJob(job, "failed", nil, "connection failed: "+err.Error())
		return
	}

	switch host.Base {
	case "ubuntu":
		err = s.deployer.ActivateUbuntu(ctx, client, closure)
	case "nixos":
		err = s.deployer.ActivateNixOS(ctx, client, closure, "switch")
	}

	if err != nil {
		s.updateJob(job, "failed", nil, "activation failed: "+err.Error())
		return
	}

	duration := time.Since(startTime)

	// Update state
	gen, _, _ := s.deployer.GetCurrentGeneration(ctx, client, host.Base)
	s.stateMgr.UpdateAfterApply(ctx, client, closure.StorePath, closure.ManifestHash, gen, duration)

	// Send webhook
	s.sendWebhook("apply", map[string]any{
		"host":       host.Name,
		"store_path": closure.StorePath,
		"duration":   duration.String(),
	})

	s.updateJob(job, "completed", map[string]any{
		"store_path": closure.StorePath,
		"generation": gen,
		"duration":   duration.String(),
	}, "")
}

func (s *Server) runDriftCheckJob(ctx context.Context, job *Job, hosts []*inventory.Host) {
	s.updateJob(job, "running", nil, "")

	results := make([]map[string]any, 0)
	totalDrift := 0

	for _, host := range hosts {
		client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
		if err != nil {
			results = append(results, map[string]any{
				"host":  host.Name,
				"error": err.Error(),
			})
			continue
		}

		hostState, err := s.stateMgr.ReadState(ctx, client)
		if err != nil || len(hostState.ManagedFiles) == 0 {
			results = append(results, map[string]any{
				"host":   host.Name,
				"status": "no managed files",
			})
			continue
		}

		driftResults, err := s.stateMgr.CheckDrift(ctx, client, hostState.ManagedFiles)
		if err != nil {
			results = append(results, map[string]any{
				"host":  host.Name,
				"error": err.Error(),
			})
			continue
		}

		driftCount := 0
		driftFiles := []string{}
		for _, r := range driftResults {
			if r.HasDrift() {
				driftCount++
				driftFiles = append(driftFiles, r.Path)
			}
		}

		// Update state
		hostState.DriftDetected = driftCount > 0
		hostState.DriftFiles = driftFiles
		hostState.LastDriftCheck = time.Now()
		s.stateMgr.WriteState(ctx, client, hostState)

		results = append(results, map[string]any{
			"host":        host.Name,
			"drift_count": driftCount,
			"drift_files": driftFiles,
		})

		totalDrift += driftCount
	}

	// Send webhook if drift detected
	if totalDrift > 0 {
		s.sendWebhook("drift", map[string]any{
			"total_drift": totalDrift,
			"hosts":       len(hosts),
		})
	}

	s.updateJob(job, "completed", map[string]any{
		"hosts":       len(hosts),
		"total_drift": totalDrift,
		"results":     results,
	}, "")
}

func (s *Server) runApplyAllJob(ctx context.Context, job *Job, hosts []*inventory.Host) {
	s.updateJob(job, "running", nil, "")

	success := 0
	failed := 0
	results := make([]map[string]any, 0)

	for _, host := range hosts {
		startTime := time.Now()

		closure, err := s.evaluator.BuildHost(ctx, host.Name, host.Base)
		if err != nil {
			results = append(results, map[string]any{
				"host":  host.Name,
				"error": "build failed: " + err.Error(),
			})
			failed++
			continue
		}

		if err := s.deployer.CopyToHost(ctx, closure, host); err != nil {
			results = append(results, map[string]any{
				"host":  host.Name,
				"error": "copy failed: " + err.Error(),
			})
			failed++
			continue
		}

		client, err := s.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
		if err != nil {
			results = append(results, map[string]any{
				"host":  host.Name,
				"error": "connection failed: " + err.Error(),
			})
			failed++
			continue
		}

		switch host.Base {
		case "ubuntu":
			err = s.deployer.ActivateUbuntu(ctx, client, closure)
		case "nixos":
			err = s.deployer.ActivateNixOS(ctx, client, closure, "switch")
		}

		if err != nil {
			results = append(results, map[string]any{
				"host":  host.Name,
				"error": "activation failed: " + err.Error(),
			})
			failed++
			continue
		}

		duration := time.Since(startTime)
		gen, _, _ := s.deployer.GetCurrentGeneration(ctx, client, host.Base)
		s.stateMgr.UpdateAfterApply(ctx, client, closure.StorePath, closure.ManifestHash, gen, duration)

		results = append(results, map[string]any{
			"host":       host.Name,
			"success":    true,
			"store_path": closure.StorePath,
			"duration":   duration.String(),
		})
		success++
	}

	s.updateJob(job, "completed", map[string]any{
		"success": success,
		"failed":  failed,
		"results": results,
	}, "")
}

// Webhook support

func (s *Server) sendWebhook(event string, data map[string]any) {
	if s.config.WebhookURL == "" {
		return
	}

	// Check if event is enabled
	enabled := false
	for _, e := range s.config.WebhookEvents {
		if e == event || e == "*" {
			enabled = true
			break
		}
	}
	if !enabled {
		return
	}

	payload := map[string]any{
		"event":     event,
		"timestamp": time.Now(),
		"data":      data,
	}

	jsonData, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", s.config.WebhookURL, strings.NewReader(string(jsonData)))
	if err != nil {
		log.Printf("Webhook error: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	if s.config.WebhookSecret != "" {
		req.Header.Set("X-Webhook-Secret", s.config.WebhookSecret)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Webhook error: %v", err)
		return
	}
	resp.Body.Close()
}

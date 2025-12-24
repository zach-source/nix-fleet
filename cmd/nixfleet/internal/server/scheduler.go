package server

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Scheduler runs periodic background tasks
type Scheduler struct {
	server *Server
	stop   chan struct{}
}

// NewScheduler creates a new scheduler
func NewScheduler(server *Server) *Scheduler {
	return &Scheduler{
		server: server,
		stop:   make(chan struct{}),
	}
}

// Start begins the scheduler goroutines
func (s *Scheduler) Start(ctx context.Context) {
	// Drift check scheduler
	if s.server.config.DriftCheckInterval > 0 {
		go s.runPeriodic(ctx, "drift-check", s.server.config.DriftCheckInterval, s.runDriftCheck)
	}

	// Update check scheduler
	if s.server.config.UpdateCheckInterval > 0 {
		go s.runPeriodic(ctx, "update-check", s.server.config.UpdateCheckInterval, s.runUpdateCheck)
	}

	// Health check scheduler
	if s.server.config.HealthCheckInterval > 0 {
		go s.runPeriodic(ctx, "health-check", s.server.config.HealthCheckInterval, s.runHealthCheck)
	}
}

// Stop halts the scheduler
func (s *Scheduler) Stop() {
	close(s.stop)
}

// runPeriodic runs a task at regular intervals
func (s *Scheduler) runPeriodic(ctx context.Context, name string, interval time.Duration, task func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("Scheduler: %s enabled (every %s)", name, interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			log.Printf("Scheduler: running %s", name)
			task(ctx)
		}
	}
}

// runDriftCheck performs drift detection on all hosts
func (s *Scheduler) runDriftCheck(ctx context.Context) {
	hosts := s.server.inventory.AllHosts()

	totalDrift := 0
	for _, host := range hosts {
		client, err := s.server.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
		if err != nil {
			log.Printf("Scheduler: drift check %s - connection failed: %v", host.Name, err)
			continue
		}

		hostState, err := s.server.stateMgr.ReadState(ctx, client)
		if err != nil || len(hostState.ManagedFiles) == 0 {
			continue
		}

		results, err := s.server.stateMgr.CheckDrift(ctx, client, hostState.ManagedFiles)
		if err != nil {
			log.Printf("Scheduler: drift check %s - check failed: %v", host.Name, err)
			continue
		}

		driftCount := 0
		driftFiles := []string{}
		for _, r := range results {
			if r.HasDrift() {
				driftCount++
				driftFiles = append(driftFiles, r.Path)
			}
		}

		// Update state
		hostState.DriftDetected = driftCount > 0
		hostState.DriftFiles = driftFiles
		hostState.LastDriftCheck = time.Now()
		s.server.stateMgr.WriteState(ctx, client, hostState)

		if driftCount > 0 {
			log.Printf("Scheduler: drift check %s - %d file(s) drifted", host.Name, driftCount)
			totalDrift += driftCount
		}
	}

	// Send webhook if drift detected
	if totalDrift > 0 {
		s.server.sendWebhook("drift", map[string]any{
			"source":      "scheduled",
			"total_drift": totalDrift,
			"hosts":       len(hosts),
		})
	}
}

// runUpdateCheck checks for pending OS updates
func (s *Scheduler) runUpdateCheck(ctx context.Context) {
	hosts := s.server.inventory.AllHosts()

	totalUpdates := 0
	totalSecurity := 0

	for _, host := range hosts {
		if host.Base != "ubuntu" {
			continue
		}

		client, err := s.server.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
		if err != nil {
			log.Printf("Scheduler: update check %s - connection failed: %v", host.Name, err)
			continue
		}

		// Check for updates using apt
		result, err := client.Exec(ctx, "apt-get update -qq && apt-get -s upgrade 2>/dev/null | grep -c '^Inst' || echo 0")
		if err != nil {
			continue
		}

		var pending int
		if _, err := fmt.Sscanf(result.Stdout, "%d", &pending); err == nil && pending > 0 {
			totalUpdates += pending
		}

		// Check security updates
		secResult, err := client.Exec(ctx, "apt-get -s upgrade 2>/dev/null | grep -c security || echo 0")
		if err == nil {
			var security int
			if _, err := fmt.Sscanf(secResult.Stdout, "%d", &security); err == nil {
				totalSecurity += security
			}
		}

		// Update state
		hostState, _ := s.server.stateMgr.ReadState(ctx, client)
		if hostState != nil {
			hostState.PendingUpdates = pending
			hostState.LastUpdateCheck = time.Now()
			s.server.stateMgr.WriteState(ctx, client, hostState)
		}
	}

	if totalUpdates > 0 {
		log.Printf("Scheduler: update check found %d pending updates (%d security)", totalUpdates, totalSecurity)
	}
}

// runHealthCheck checks host connectivity and service health
func (s *Scheduler) runHealthCheck(ctx context.Context) {
	hosts := s.server.inventory.AllHosts()

	online := 0
	offline := 0
	unhealthy := 0

	for _, host := range hosts {
		client, err := s.server.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
		if err != nil {
			offline++
			log.Printf("Scheduler: health check %s - offline: %v", host.Name, err)
			continue
		}

		online++

		// Check system status
		result, err := client.Exec(ctx, "systemctl is-system-running 2>/dev/null || echo unknown")
		if err != nil {
			continue
		}

		status := result.Stdout
		if status != "running\n" && status != "degraded\n" {
			unhealthy++
			log.Printf("Scheduler: health check %s - status: %s", host.Name, status)
		}

		// Check reboot required
		reboot, _ := s.server.deployer.CheckRebootNeeded(ctx, client, host.Base)
		if reboot {
			hostState, _ := s.server.stateMgr.ReadState(ctx, client)
			if hostState != nil {
				hostState.RebootRequired = true
				s.server.stateMgr.WriteState(ctx, client, hostState)
			}
		}
	}

	log.Printf("Scheduler: health check - %d online, %d offline, %d unhealthy", online, offline, unhealthy)

	// Send webhook if hosts are offline or unhealthy
	if offline > 0 || unhealthy > 0 {
		s.server.sendWebhook("health", map[string]any{
			"source":    "scheduled",
			"online":    online,
			"offline":   offline,
			"unhealthy": unhealthy,
		})
	}
}

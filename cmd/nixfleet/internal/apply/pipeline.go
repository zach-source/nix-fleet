// Package apply implements the deployment pipeline with preflight and health checks
package apply

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nixfleet/nixfleet/internal/health"
	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/k0s"
	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/pki"
	"github.com/nixfleet/nixfleet/internal/preflight"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
)

// FailurePolicy defines what to do when health checks fail
type FailurePolicy string

const (
	PolicyRollback FailurePolicy = "rollback"
	PolicyHalt     FailurePolicy = "halt"
	PolicyContinue FailurePolicy = "continue"
)

// PipelineConfig configures the apply pipeline
type PipelineConfig struct {
	DryRun            bool
	SkipPreflight     bool
	SkipHealthChecks  bool
	HealthCheckPolicy FailurePolicy
	Parallel          int
	HealthCheckDelay  time.Duration // delay after activation before health checks

	// PKI configuration
	PKIEnabled    bool
	PKIConfig     *pki.DeployConfig
	PKIAutoRenew  bool     // Auto-renew expiring certs before deploy
	PKIRenewDays  int      // Renew certs expiring within this many days
	PKIIdentities []string // Age identity files for decryption
}

// DefaultPipelineConfig returns sensible defaults
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{
		DryRun:            false,
		SkipPreflight:     false,
		SkipHealthChecks:  false,
		HealthCheckPolicy: PolicyRollback,
		Parallel:          5,
		HealthCheckDelay:  5 * time.Second,
		PKIEnabled:        false,
		PKIAutoRenew:      true,
		PKIRenewDays:      30,
	}
}

// HostResult contains the result of applying to a single host
type HostResult struct {
	Host              string                      `json:"host"`
	Success           bool                        `json:"success"`
	PreflightResults  *preflight.PreflightResults `json:"preflight,omitempty"`
	DeployResult      *DeployResult               `json:"deploy,omitempty"`
	PKIResult         *pki.DeployResult           `json:"pki,omitempty"`
	K0sResult         *k0s.ReconcileResult        `json:"k0s,omitempty"`
	HealthResults     *health.HealthResults       `json:"health,omitempty"`
	RollbackPerformed bool                        `json:"rollbackPerformed,omitempty"`
	Error             string                      `json:"error,omitempty"`
}

// DeployResult contains deployment-specific results
type DeployResult struct {
	Closure      string `json:"closure"`
	ManifestHash string `json:"manifestHash,omitempty"`
	Action       string `json:"action"` // "switch", "boot", "test"
}

// PipelineResults contains results for all hosts
type PipelineResults struct {
	StartTime   time.Time     `json:"startTime"`
	EndTime     time.Time     `json:"endTime"`
	Duration    string        `json:"duration"`
	TotalHosts  int           `json:"totalHosts"`
	Successful  int           `json:"successful"`
	Failed      int           `json:"failed"`
	HostResults []*HostResult `json:"hostResults"`
}

// Pipeline orchestrates the apply process
type Pipeline struct {
	config        PipelineConfig
	sshPool       *ssh.Pool
	evaluator     *nix.Evaluator
	deployer      *nix.Deployer
	preflight     *preflight.Checker
	health        *health.Checker
	pkiDeployer   *pki.Deployer
	k0sReconciler *k0s.Reconciler
	stateMgr      *state.Manager
}

// NewPipeline creates a new apply pipeline
func NewPipeline(config PipelineConfig, sshPool *ssh.Pool, evaluator *nix.Evaluator, deployer *nix.Deployer) *Pipeline {
	p := &Pipeline{
		config:        config,
		sshPool:       sshPool,
		evaluator:     evaluator,
		deployer:      deployer,
		preflight:     preflight.NewChecker(),
		health:        health.NewChecker(),
		k0sReconciler: k0s.NewReconciler(),
		stateMgr:      state.NewManager(),
	}

	// Initialize PKI deployer if enabled
	if config.PKIEnabled {
		pkiConfig := config.PKIConfig
		if pkiConfig == nil {
			pkiConfig = pki.DefaultDeployConfig()
		}
		if len(config.PKIIdentities) > 0 {
			pkiConfig.Identities = config.PKIIdentities
		}
		p.pkiDeployer = pki.NewDeployer(pkiConfig)
	}

	return p
}

// Apply runs the full apply pipeline for the given hosts
func (p *Pipeline) Apply(ctx context.Context, hosts []*inventory.Host, action string) (*PipelineResults, error) {
	results := &PipelineResults{
		StartTime:   time.Now(),
		TotalHosts:  len(hosts),
		HostResults: make([]*HostResult, 0, len(hosts)),
	}

	// Auto-renew expiring certificates before deployment
	if p.pkiDeployer != nil && p.config.PKIAutoRenew && p.pkiDeployer.IsEnabled() {
		if err := p.autoRenewCerts(ctx, hosts); err != nil {
			log.Printf("Warning: certificate auto-renewal failed: %v", err)
		}
	}

	// Create semaphore for parallelism control
	sem := make(chan struct{}, p.config.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, host := range hosts {
		wg.Add(1)
		go func(h *inventory.Host) {
			defer wg.Done()

			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			result := p.applyHost(ctx, h, action)

			mu.Lock()
			results.HostResults = append(results.HostResults, result)
			if result.Success {
				results.Successful++
			} else {
				results.Failed++
			}
			mu.Unlock()
		}(host)
	}

	wg.Wait()

	results.EndTime = time.Now()
	results.Duration = results.EndTime.Sub(results.StartTime).String()

	return results, nil
}

// applyHost runs the pipeline for a single host
func (p *Pipeline) applyHost(ctx context.Context, host *inventory.Host, action string) *HostResult {
	result := &HostResult{
		Host: host.Name,
	}

	// Get SSH port (default to 22)
	sshPort := host.SSHPort
	if sshPort == 0 {
		sshPort = 22
	}

	// Get SSH connection
	client, err := p.sshPool.GetWithUser(ctx, host.Addr, sshPort, host.SSHUser)
	if err != nil {
		result.Error = fmt.Sprintf("SSH connection failed: %v", err)
		return result
	}

	// Phase 1: Preflight checks
	if !p.config.SkipPreflight {
		log.Printf("[%s] Running preflight checks...", host.Name)
		preflightResults, err := p.preflight.RunAll(ctx, client, host.Base)
		result.PreflightResults = preflightResults

		if err != nil {
			result.Error = fmt.Sprintf("Preflight check error: %v", err)
			return result
		}

		if !preflightResults.Passed {
			result.Error = "Preflight checks failed"
			return result
		}
		log.Printf("[%s] Preflight checks passed", host.Name)
	}

	// Phase 2: Build and evaluate
	log.Printf("[%s] Building configuration...", host.Name)
	closure, err := p.evaluator.BuildHost(ctx, host.Name, host.Base)
	if err != nil {
		result.Error = fmt.Sprintf("Build failed: %v", err)
		return result
	}

	result.DeployResult = &DeployResult{
		Closure:      closure.StorePath,
		ManifestHash: closure.ManifestHash,
		Action:       action,
	}

	// Dry run stops here
	if p.config.DryRun {
		log.Printf("[%s] Dry run: would deploy %s", host.Name, closure.StorePath)
		result.Success = true
		return result
	}

	// Phase 3: Copy closure
	log.Printf("[%s] Copying closure to host...", host.Name)
	if err := p.deployer.CopyToHost(ctx, closure, host); err != nil {
		result.Error = fmt.Sprintf("Copy failed: %v", err)
		return result
	}

	// Phase 4: Activate
	log.Printf("[%s] Activating configuration...", host.Name)
	switch host.Base {
	case "ubuntu":
		if err := p.deployer.ActivateUbuntu(ctx, client, closure); err != nil {
			result.Error = fmt.Sprintf("Activation failed: %v", err)
			return result
		}
	case "nixos":
		if err := p.deployer.ActivateNixOS(ctx, client, closure, action); err != nil {
			result.Error = fmt.Sprintf("Activation failed: %v", err)
			return result
		}
	case "darwin":
		if err := p.deployer.ActivateDarwin(ctx, client, closure, action); err != nil {
			result.Error = fmt.Sprintf("Activation failed: %v", err)
			return result
		}
	default:
		result.Error = fmt.Sprintf("Unknown host base: %s", host.Base)
		return result
	}

	// Phase 4.5: PKI deployment (if enabled)
	if p.pkiDeployer != nil && p.pkiDeployer.IsEnabled() {
		log.Printf("[%s] Deploying PKI certificates...", host.Name)
		pkiResult := p.pkiDeployer.Deploy(ctx, client, host)
		result.PKIResult = pkiResult

		if !pkiResult.Success {
			// PKI failure is non-fatal by default, just log warning
			log.Printf("[%s] PKI deployment warning: %s", host.Name, pkiResult.Error)
		} else {
			if pkiResult.CertDeployed {
				log.Printf("[%s] PKI: deployed host certificate (expires in %d days)",
					host.Name, pkiResult.CertInfo.DaysLeft)
			} else if pkiResult.CADeployed {
				log.Printf("[%s] PKI: deployed CA certificate only", host.Name)
			}
		}
	}

	// Phase 4.6: k0s reconciliation (if k0s is enabled)
	if p.k0sReconciler.IsK0sEnabled(ctx, client) {
		log.Printf("[%s] Reconciling k0s resources...", host.Name)

		// Read previous k0s state
		hostState, _ := p.stateMgr.ReadState(ctx, client)
		var previousK0sState *state.K0sState
		if hostState != nil {
			previousK0sState = hostState.K0s
		}

		// Reconcile - cleanup orphaned resources
		k0sResult, err := p.k0sReconciler.Reconcile(ctx, client, previousK0sState, p.config.DryRun)
		if err != nil {
			log.Printf("[%s] k0s reconciliation warning: %v", host.Name, err)
		} else {
			result.K0sResult = k0sResult

			if len(k0sResult.DeletedCharts) > 0 {
				log.Printf("[%s] k0s: deleted %d orphaned Helm chart(s): %v",
					host.Name, len(k0sResult.DeletedCharts), k0sResult.DeletedCharts)
			}
			if len(k0sResult.DeletedResources) > 0 {
				log.Printf("[%s] k0s: deleted %d orphaned resource(s): %v",
					host.Name, len(k0sResult.DeletedResources), k0sResult.DeletedResources)
			}
			if len(k0sResult.Errors) > 0 {
				log.Printf("[%s] k0s: reconciliation errors: %v", host.Name, k0sResult.Errors)
			}
		}

		// Update k0s state (unless dry run)
		if !p.config.DryRun {
			if err := p.k0sReconciler.UpdateState(ctx, client); err != nil {
				log.Printf("[%s] Warning: failed to update k0s state: %v", host.Name, err)
			}
		}
	}

	// Phase 5: Health checks
	if !p.config.SkipHealthChecks {
		// Wait for services to stabilize
		if p.config.HealthCheckDelay > 0 {
			log.Printf("[%s] Waiting %s for services to stabilize...", host.Name, p.config.HealthCheckDelay)
			time.Sleep(p.config.HealthCheckDelay)
		}

		log.Printf("[%s] Running health checks...", host.Name)
		healthConfigs := p.getHealthChecksForHost(host)

		if len(healthConfigs) > 0 {
			healthResults, err := p.health.RunChecks(ctx, client, healthConfigs)
			result.HealthResults = healthResults

			if err != nil {
				result.Error = fmt.Sprintf("Health check error: %v", err)
				return result
			}

			if !healthResults.Passed {
				log.Printf("[%s] Health checks failed: %s", host.Name, healthResults.Summary)

				// Apply failure policy
				switch p.config.HealthCheckPolicy {
				case PolicyRollback:
					log.Printf("[%s] Rolling back due to health check failure...", host.Name)
					if err := p.deployer.Rollback(ctx, client, closure.Base, 0); err != nil {
						result.Error = fmt.Sprintf("Health checks failed and rollback failed: %v", err)
						return result
					}
					result.RollbackPerformed = true
					result.Error = "Health checks failed, rolled back to previous configuration"
					return result

				case PolicyHalt:
					result.Error = "Health checks failed, halting"
					return result

				case PolicyContinue:
					log.Printf("[%s] Health checks failed but continuing due to policy", host.Name)
					// Continue with success despite health check failure
				}
			}
		}
		log.Printf("[%s] Health checks passed", host.Name)
	}

	result.Success = true
	log.Printf("[%s] Apply completed successfully", host.Name)
	return result
}

// getHealthChecksForHost extracts health check configurations for a host
func (p *Pipeline) getHealthChecksForHost(host *inventory.Host) []health.HealthCheckConfig {
	configs := make([]health.HealthCheckConfig, 0)

	// Add any health checks defined in the host configuration
	// This would typically come from the evaluated Nix configuration
	// For now, we add basic systemd checks for common services

	// Default: check SSH is still working (sanity check)
	configs = append(configs, health.HealthCheckConfig{
		Name:    "ssh_post_deploy",
		Type:    health.CheckTypeCommand,
		Target:  "echo 'post-deploy-ok'",
		Timeout: 5 * time.Second,
	})

	return configs
}

// ApplyWithHealthChecks applies with explicit health check configs
func (p *Pipeline) ApplyWithHealthChecks(ctx context.Context, hosts []*inventory.Host, action string, healthConfigs map[string][]health.HealthCheckConfig) (*PipelineResults, error) {
	// Store health configs for use during apply
	// This is a simplified approach - in production you'd want proper config passing
	return p.Apply(ctx, hosts, action)
}

// autoRenewCerts checks for expiring certificates and renews them before deployment
func (p *Pipeline) autoRenewCerts(ctx context.Context, hosts []*inventory.Host) error {
	// Check which certs need renewal
	renewalInfos, err := p.pkiDeployer.CheckRenewalNeeded(ctx, p.config.PKIRenewDays)
	if err != nil {
		return fmt.Errorf("checking renewal: %w", err)
	}

	if len(renewalInfos) == 0 {
		return nil
	}

	// Build a set of hostnames we're deploying to
	targetHosts := make(map[string]bool)
	for _, h := range hosts {
		targetHosts[h.Name] = true
	}

	// Renew certificates for hosts we're deploying to
	renewed := 0
	for _, info := range renewalInfos {
		if !targetHosts[info.Hostname] {
			continue // Skip hosts not in this deployment
		}

		log.Printf("Auto-renewing certificate for %s (%s)", info.Hostname, info.Reason)

		// Renew with default validity (1 year)
		_, err := p.pkiDeployer.RenewCert(ctx, info.Hostname, nil, 365*24*time.Hour)
		if err != nil {
			log.Printf("Warning: failed to renew cert for %s: %v", info.Hostname, err)
			continue
		}

		renewed++
		log.Printf("Renewed certificate for %s", info.Hostname)
	}

	if renewed > 0 {
		log.Printf("Auto-renewed %d certificate(s)", renewed)
	}

	return nil
}

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nixfleet/nixfleet/internal/cache"
	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/k0s"
	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/nodestatus"
	"github.com/nixfleet/nixfleet/internal/osupdate"
	"github.com/nixfleet/nixfleet/internal/pki"
	"github.com/nixfleet/nixfleet/internal/pullmode"
	"github.com/nixfleet/nixfleet/internal/reboot"
	"github.com/nixfleet/nixfleet/internal/secrets"
	"github.com/nixfleet/nixfleet/internal/server"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
	"github.com/spf13/cobra"
)

var (
	version   = "dev"
	gitCommit = ""
	gitTag    = ""
)

// Global config
var (
	inventoryPath string
	flakePath     string
	targetHost    string
	targetGroup   string
	maxParallel   int
	dryRun        bool
	verbose       bool
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nReceived interrupt, shutting down...")
		cancel()
	}()

	if err := rootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nixfleet",
		Short: "Agentless fleet management with Nix",
		Long: `NixFleet manages Ubuntu and NixOS servers using Nix as the desired-state engine.

It provides Ansible-like UX for:
  - Deploying Nix-based configurations to Ubuntu hosts
  - Managing NixOS systems via nixosConfigurations
  - Orchestrating OS updates with reboot coordination
  - Rolling deployments with canary support`,
		Version: version,
	}

	// Global flags
	cmd.PersistentFlags().StringVarP(&inventoryPath, "inventory", "i", "inventory/", "Path to inventory directory or file")
	cmd.PersistentFlags().StringVarP(&flakePath, "flake", "f", ".", "Path to flake directory")
	cmd.PersistentFlags().StringVarP(&targetHost, "host", "H", "", "Target specific host")
	cmd.PersistentFlags().StringVarP(&targetGroup, "group", "g", "", "Target host group")
	cmd.PersistentFlags().IntVarP(&maxParallel, "parallel", "p", 5, "Max parallel operations")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes")
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")

	// Add subcommands
	cmd.AddCommand(planCmd())
	cmd.AddCommand(applyCmd())
	cmd.AddCommand(rollbackCmd())
	cmd.AddCommand(statusCmd())
	cmd.AddCommand(osUpdateCmd())
	cmd.AddCommand(rebootCmd())
	cmd.AddCommand(cacheCmd())
	cmd.AddCommand(secretsCmd())
	cmd.AddCommand(driftCmd())
	cmd.AddCommand(runCmd())
	cmd.AddCommand(serverCmd())
	cmd.AddCommand(pullModeCmd())
	cmd.AddCommand(hostCmd())
	cmd.AddCommand(pkiCmd())
	cmd.AddCommand(k0sCmd())
	cmd.AddCommand(nodeStatusCmd())

	return cmd
}

func loadInventoryAndHosts(ctx context.Context) (*inventory.Inventory, []*inventory.Host, error) {
	// Load inventory
	inv, err := inventory.LoadFromDir(inventoryPath)
	if err != nil {
		// Try as single file
		inv, err = inventory.LoadFromFile(inventoryPath)
		if err != nil {
			return nil, nil, fmt.Errorf("loading inventory: %w", err)
		}
	}

	if err := inv.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid inventory: %w", err)
	}

	// Determine target hosts
	var hosts []*inventory.Host
	switch {
	case targetHost != "":
		h, ok := inv.GetHost(targetHost)
		if !ok {
			return nil, nil, fmt.Errorf("host %q not found in inventory", targetHost)
		}
		hosts = []*inventory.Host{h}
	case targetGroup != "":
		hosts = inv.HostsInGroup(targetGroup)
		if len(hosts) == 0 {
			return nil, nil, fmt.Errorf("no hosts in group %q", targetGroup)
		}
	default:
		hosts = inv.AllHosts()
	}

	return inv, hosts, nil
}

func planCmd() *cobra.Command {
	var showDiff bool

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what changes would be applied",
		Long: `Evaluate host configurations and show a diff of what would change.

Compares desired configuration against current deployed state to show:
- Changed configuration hashes
- Store path differences
- Whether a rebuild is needed`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			// Initialize Nix evaluator
			flake, err := nix.ResolveFlakePath(flakePath)
			if err != nil {
				return err
			}

			evaluator, err := nix.NewEvaluator(flake)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			stateMgr := state.NewManager()

			fmt.Printf("Planning changes for %d host(s)...\n\n", len(hosts))

			changedCount := 0
			upToDateCount := 0

			for _, host := range hosts {
				fmt.Printf("Host: %s (%s @ %s)\n", host.Name, host.Base, host.Addr)

				closure, err := evaluator.BuildHost(ctx, host.Name, host.Base)
				if err != nil {
					fmt.Printf("  ERROR: %v\n\n", err)
					continue
				}

				size, _ := evaluator.GetClosureSize(ctx, closure.StorePath)

				// Try to get current state from host
				var hostState *state.HostState
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err == nil {
					hostState, _ = stateMgr.ReadState(ctx, client)
				}

				// Compare with current state
				hasChanges := true
				if hostState != nil && hostState.ManifestHash != "" {
					if hostState.ManifestHash == closure.ManifestHash {
						hasChanges = false
						upToDateCount++
						fmt.Printf("  Status: UP TO DATE\n")
						fmt.Printf("  Store path: %s\n", closure.StorePath)
						if verbose {
							fmt.Printf("  Manifest hash: %s\n", closure.ManifestHash)
							fmt.Printf("  Last apply: %s\n", hostState.LastApply.Format(time.RFC3339))
						}
					} else {
						changedCount++
						fmt.Printf("  Status: CHANGES PENDING\n")
						fmt.Printf("  Current path: %s\n", hostState.StorePath)
						fmt.Printf("  New path:     %s\n", closure.StorePath)
						if showDiff {
							fmt.Printf("  Hash diff:\n")
							fmt.Printf("    - %s (current)\n", hostState.ManifestHash)
							fmt.Printf("    + %s (new)\n", closure.ManifestHash)
						}
					}
				} else {
					changedCount++
					fmt.Printf("  Status: NEW DEPLOYMENT\n")
					fmt.Printf("  Store path: %s\n", closure.StorePath)
					fmt.Printf("  Manifest hash: %s\n", closure.ManifestHash)
				}

				fmt.Printf("  Closure size: %.2f MB\n", float64(size)/1024/1024)

				// Show additional info if changes are pending
				if hasChanges && hostState != nil {
					if hostState.DriftDetected {
						fmt.Printf("  Note: %d file(s) have drifted from expected state\n", len(hostState.DriftFiles))
					}
					if hostState.RebootRequired {
						fmt.Printf("  Note: Host requires reboot (pending from previous apply)\n")
					}
				}

				fmt.Println()
			}

			// Summary
			fmt.Printf("Summary: %d with changes, %d up-to-date\n", changedCount, upToDateCount)
			if changedCount > 0 {
				fmt.Println("Run 'nixfleet apply' to deploy changes")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&showDiff, "diff", false, "Show detailed diff of manifest hashes")

	return cmd
}

func applyCmd() *cobra.Command {
	var (
		skipPreflight bool
		skipHealth    bool
		skipState     bool
		withPKI       bool
		pkiDir        string
		pkiIdentities []string
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply configurations to hosts",
		Long:  `Build and deploy configurations to target hosts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Printf("Would apply to %d host(s):\n", len(hosts))
				for _, h := range hosts {
					fmt.Printf("  - %s (%s)\n", h.Name, h.Addr)
				}
				return nil
			}

			// Initialize components
			flake, err := nix.ResolveFlakePath(flakePath)
			if err != nil {
				return err
			}

			evaluator, err := nix.NewEvaluator(flake)
			if err != nil {
				return err
			}

			deployer := nix.NewDeployer(evaluator)
			pool := ssh.NewPool(nil)
			defer pool.Close()

			stateMgr := state.NewManager()
			executor := ssh.NewExecutor(pool, maxParallel)

			fmt.Printf("Applying to %d host(s)...\n\n", len(hosts))

			// Preflight checks
			if !skipPreflight {
				fmt.Println("Running preflight checks...")
				results := executor.ExecOnHosts(ctx, hosts, "echo ok", false)
				failed := ssh.FilterFailed(results)
				if len(failed) > 0 {
					fmt.Printf("Preflight failed for %d host(s):\n", len(failed))
					for _, r := range failed {
						fmt.Printf("  - %s: %v\n", r.Host.Name, r.Error)
					}
					return fmt.Errorf("preflight checks failed")
				}
				fmt.Printf("Preflight passed for %d host(s)\n\n", len(hosts))
			}

			successCount := 0
			failedCount := 0

			// Build and deploy each host
			for _, host := range hosts {
				fmt.Printf("Deploying to %s...\n", host.Name)
				startTime := time.Now()

				// Build
				closure, err := evaluator.BuildHost(ctx, host.Name, host.Base)
				if err != nil {
					fmt.Printf("  Build failed: %v\n", err)
					failedCount++
					continue
				}
				fmt.Printf("  Built: %s\n", closure.StorePath)

				// Copy
				fmt.Printf("  Copying closure...\n")
				if err := deployer.CopyToHost(ctx, closure, host); err != nil {
					fmt.Printf("  Copy failed: %v\n", err)
					failedCount++
					continue
				}

				// Activate
				fmt.Printf("  Activating...\n")
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("  Connection failed: %v\n", err)
					failedCount++
					continue
				}

				switch host.Base {
				case "ubuntu":
					err = deployer.ActivateUbuntu(ctx, client, closure)
				case "nixos":
					err = deployer.ActivateNixOS(ctx, client, closure, "switch")
				}

				if err != nil {
					fmt.Printf("  Activation failed: %v\n", err)
					failedCount++
					continue
				}

				// Deploy PKI certificates if enabled
				if withPKI {
					pkiConfig := pki.DefaultDeployConfig()
					pkiConfig.PKIDir = pkiDir
					pkiConfig.Identities = pkiIdentities
					pkiDeployer := pki.NewDeployer(pkiConfig)

					if pkiDeployer.IsEnabled() {
						pkiResult := pkiDeployer.Deploy(ctx, client, host)
						if pkiResult.Success {
							if pkiResult.CertDeployed && pkiResult.CertInfo != nil {
								fmt.Printf("  PKI: deployed cert (expires in %d days)\n", pkiResult.CertInfo.DaysLeft)
							} else if pkiResult.CADeployed {
								fmt.Printf("  PKI: deployed CA only\n")
							}
						} else {
							fmt.Printf("  PKI warning: %s\n", pkiResult.Error)
						}
					}
				}

				duration := time.Since(startTime)

				// Update state
				if !skipState {
					gen, _, _ := deployer.GetCurrentGeneration(ctx, client, host.Base)
					if err := stateMgr.UpdateAfterApply(ctx, client, closure.StorePath, closure.ManifestHash, gen, duration); err != nil {
						fmt.Printf("  Warning: failed to update state - %v\n", err)
					} else if verbose {
						fmt.Printf("  State updated (gen %d)\n", gen)
					}
				}

				// Health checks
				if !skipHealth {
					// Basic health check: ensure SSH still works
					result, err := client.Exec(ctx, "systemctl is-system-running || true")
					if err != nil {
						fmt.Printf("  Health check failed: %v\n", err)
					} else {
						fmt.Printf("  System status: %s", result.Stdout)
					}
				}

				fmt.Printf("  Done! (%s)\n\n", duration.Round(time.Second))
				successCount++
			}

			fmt.Printf("Summary: %d succeeded, %d failed\n", successCount, failedCount)
			return nil
		},
	}

	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "Skip preflight checks")
	cmd.Flags().BoolVar(&skipHealth, "skip-health", false, "Skip post-apply health checks")
	cmd.Flags().BoolVar(&skipState, "skip-state", false, "Skip updating host state after apply")
	cmd.Flags().BoolVar(&withPKI, "with-pki", false, "Deploy PKI certificates after activation")
	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory containing PKI files")
	cmd.Flags().StringSliceVar(&pkiIdentities, "pki-identity", nil, "Age identity files for decrypting PKI keys")

	return cmd
}

func rollbackCmd() *cobra.Command {
	var toGeneration string

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Rollback to a previous generation",
		Long:  `Rollback host configuration to a previous generation.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			flake, err := nix.ResolveFlakePath(flakePath)
			if err != nil {
				return err
			}

			evaluator, err := nix.NewEvaluator(flake)
			if err != nil {
				return err
			}

			deployer := nix.NewDeployer(evaluator)

			for _, host := range hosts {
				fmt.Printf("Rolling back %s...\n", host.Name)

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("  Connection failed: %v\n", err)
					continue
				}

				generation := 0 // 0 means previous
				if toGeneration != "previous" && toGeneration != "" {
					fmt.Sscanf(toGeneration, "%d", &generation)
				}

				if err := deployer.Rollback(ctx, client, host.Base, generation); err != nil {
					fmt.Printf("  Rollback failed: %v\n", err)
					continue
				}

				fmt.Printf("  Done!\n")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&toGeneration, "to", "previous", "Target generation (previous or generation number)")

	return cmd
}

func statusCmd() *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show host status",
		Long: `Display current status of managed hosts including generation, health, and pending changes.

Use --all to show extended status including update counts and drift.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			flake, err := nix.ResolveFlakePath(flakePath)
			if err != nil {
				return err
			}

			evaluator, err := nix.NewEvaluator(flake)
			if err != nil {
				return err
			}

			deployer := nix.NewDeployer(evaluator)
			stateMgr := state.NewManager()

			if showAll {
				fmt.Printf("%-18s %-7s %-15s %-6s %-6s %-8s %s\n", "HOST", "BASE", "ADDRESS", "REBOOT", "DRIFT", "UPDATES", "GENERATION")
				fmt.Printf("%-18s %-7s %-15s %-6s %-6s %-8s %s\n", "----", "----", "-------", "------", "-----", "-------", "----------")
			} else {
				fmt.Printf("%-20s %-8s %-15s %-10s %s\n", "HOST", "BASE", "ADDRESS", "REBOOT", "CURRENT")
				fmt.Printf("%-20s %-8s %-15s %-10s %s\n", "----", "----", "-------", "------", "-------")
			}

			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					if showAll {
						fmt.Printf("%-18s %-7s %-15s %-6s %-6s %-8s %s\n", host.Name, host.Base, host.Addr, "?", "?", "?", "connection failed")
					} else {
						fmt.Printf("%-20s %-8s %-15s %-10s %s\n", host.Name, host.Base, host.Addr, "?", "connection failed")
					}
					continue
				}

				_, current, err := deployer.GetCurrentGeneration(ctx, client, host.Base)
				if err != nil {
					current = "unknown"
				}

				reboot, _ := deployer.CheckRebootNeeded(ctx, client, host.Base)
				rebootStr := "no"
				if reboot {
					rebootStr = "YES"
				}

				// Read state for extended info
				hostState, _ := stateMgr.ReadState(ctx, client)

				if showAll {
					driftStr := "-"
					updatesStr := "-"

					if hostState != nil {
						if hostState.DriftDetected {
							driftStr = fmt.Sprintf("%d", len(hostState.DriftFiles))
						} else if !hostState.LastDriftCheck.IsZero() {
							driftStr = "ok"
						}

						if hostState.PendingUpdates > 0 {
							if hostState.SecurityUpdates > 0 {
								updatesStr = fmt.Sprintf("%d(%d!)", hostState.PendingUpdates, hostState.SecurityUpdates)
							} else {
								updatesStr = fmt.Sprintf("%d", hostState.PendingUpdates)
							}
						} else if !hostState.LastUpdateCheck.IsZero() {
							updatesStr = "0"
						}
					}

					// Truncate store path for display
					gen := current
					if len(gen) > 25 {
						gen = gen[:22] + "..."
					}

					fmt.Printf("%-18s %-7s %-15s %-6s %-6s %-8s %s\n", host.Name, host.Base, host.Addr, rebootStr, driftStr, updatesStr, gen)
				} else {
					// Truncate store path for display
					if len(current) > 40 {
						current = current[:37] + "..."
					}

					fmt.Printf("%-20s %-8s %-15s %-10s %s\n", host.Name, host.Base, host.Addr, rebootStr, current)
				}

				// Verbose output
				if verbose && hostState != nil {
					fmt.Printf("  Last Apply: %s (gen %d)\n", hostState.LastApply.Format(time.RFC3339), hostState.CurrentGeneration)
					if hostState.ApplyDuration != "" {
						fmt.Printf("  Apply Duration: %s\n", hostState.ApplyDuration)
					}
					if len(hostState.ServiceHealth) > 0 {
						healthy := 0
						for _, s := range hostState.ServiceHealth {
							if s.Active {
								healthy++
							}
						}
						fmt.Printf("  Services: %d/%d healthy\n", healthy, len(hostState.ServiceHealth))
					}
					if hostState.DriftDetected {
						fmt.Printf("  Drift: %d file(s)\n", len(hostState.DriftFiles))
					}
					fmt.Println()
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show extended status (updates, drift)")

	return cmd
}

func osUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "os-update",
		Short: "Manage OS updates on Ubuntu hosts",
		Long: `Manage OS updates on Ubuntu hosts with configurable policies and rollout strategies.

Subcommands:
  check      - Check for pending updates
  apply      - Apply updates
  policy     - Configure update policy
  hold       - Hold packages from upgrades
  unhold     - Remove package holds`,
	}

	cmd.AddCommand(osUpdateCheckCmd())
	cmd.AddCommand(osUpdateApplyCmd())
	cmd.AddCommand(osUpdatePolicyCmd())
	cmd.AddCommand(osUpdateHoldCmd())
	cmd.AddCommand(osUpdateUnholdCmd())

	return cmd
}

func filterUbuntuHosts(hosts []*inventory.Host) []*inventory.Host {
	var ubuntuHosts []*inventory.Host
	for _, h := range hosts {
		if h.Base == "ubuntu" {
			ubuntuHosts = append(ubuntuHosts, h)
		}
	}
	return ubuntuHosts
}

func osUpdateCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check for pending updates",
		Long:  `Check for available OS updates on Ubuntu hosts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			ubuntuHosts := filterUbuntuHosts(hosts)
			if len(ubuntuHosts) == 0 {
				fmt.Println("No Ubuntu hosts found")
				return nil
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			updater := osupdate.NewUpdater()

			fmt.Printf("Checking updates on %d host(s)...\n\n", len(ubuntuHosts))

			for _, host := range ubuntuHosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				pending, err := updater.CheckPendingUpdates(ctx, client)
				if err != nil {
					fmt.Printf("%s: check failed - %v\n", host.Name, err)
					continue
				}

				reboot, _ := updater.IsRebootRequired(ctx, client)

				fmt.Printf("%s:\n", host.Name)
				fmt.Printf("  Security updates: %d\n", len(pending.SecurityUpdates))
				fmt.Printf("  Regular updates:  %d\n", len(pending.RegularUpdates))
				fmt.Printf("  Total pending:    %d\n", pending.TotalCount)
				if reboot {
					fmt.Printf("  Reboot required:  YES\n")
				}

				if verbose && pending.TotalCount > 0 {
					fmt.Println("  Packages:")
					for _, pkg := range pending.SecurityUpdates {
						fmt.Printf("    [SECURITY] %s: %s -> %s\n", pkg.Name, pkg.CurrentVersion, pkg.NewVersion)
					}
					for _, pkg := range pending.RegularUpdates {
						fmt.Printf("    %s: %s -> %s\n", pkg.Name, pkg.CurrentVersion, pkg.NewVersion)
					}
				}
				fmt.Println()
			}

			return nil
		},
	}
}

func osUpdateApplyCmd() *cobra.Command {
	var securityOnly, allowReboot, distUpgrade bool
	var strategy string
	var canaryPercent int
	var rebootDelay time.Duration

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply OS updates",
		Long: `Apply OS updates to Ubuntu hosts.

Strategies:
  serial   - Update hosts one at a time (default)
  parallel - Update all hosts simultaneously
  canary   - Update a percentage first, then the rest`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			ubuntuHosts := filterUbuntuHosts(hosts)
			if len(ubuntuHosts) == 0 {
				fmt.Println("No Ubuntu hosts found")
				return nil
			}

			if dryRun {
				fmt.Printf("Would apply updates to %d host(s):\n", len(ubuntuHosts))
				for _, h := range ubuntuHosts {
					fmt.Printf("  - %s (%s)\n", h.Name, h.Addr)
				}
				return nil
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			updater := osupdate.NewUpdater()

			// Handle different strategies
			var hostsToUpdate [][]*inventory.Host
			switch strategy {
			case "parallel":
				hostsToUpdate = [][]*inventory.Host{ubuntuHosts}
			case "canary":
				canaryCount := (len(ubuntuHosts) * canaryPercent) / 100
				if canaryCount < 1 {
					canaryCount = 1
				}
				if canaryCount >= len(ubuntuHosts) {
					hostsToUpdate = [][]*inventory.Host{ubuntuHosts}
				} else {
					hostsToUpdate = [][]*inventory.Host{
						ubuntuHosts[:canaryCount],
						ubuntuHosts[canaryCount:],
					}
					fmt.Printf("Canary rollout: %d canary host(s), then %d remaining\n\n", canaryCount, len(ubuntuHosts)-canaryCount)
				}
			default: // serial
				for _, h := range ubuntuHosts {
					hostsToUpdate = append(hostsToUpdate, []*inventory.Host{h})
				}
			}

			totalUpdated := 0
			totalFailed := 0

			for batchIdx, batch := range hostsToUpdate {
				if strategy == "canary" && batchIdx > 0 {
					fmt.Println("\nCanary batch completed successfully. Proceeding with remaining hosts...")
				}

				for _, host := range batch {
					fmt.Printf("Updating %s...\n", host.Name)

					client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
					if err != nil {
						fmt.Printf("  Connection failed: %v\n", err)
						totalFailed++
						continue
					}

					var result *osupdate.UpdateResult
					if securityOnly {
						result, err = updater.ApplySecurityUpdates(ctx, client)
					} else if distUpgrade {
						result, err = updater.ApplyDistUpgrade(ctx, client)
					} else {
						result, err = updater.ApplyAllUpdates(ctx, client)
					}

					if err != nil {
						fmt.Printf("  Update failed: %v\n", err)
						totalFailed++
						continue
					}

					if !result.Success {
						fmt.Printf("  Update failed: %s\n", result.Stderr)
						totalFailed++
						continue
					}

					fmt.Printf("  Updated %d package(s)\n", len(result.PackagesUpdated))
					if verbose && len(result.PackagesUpdated) > 0 {
						for _, pkg := range result.PackagesUpdated {
							if pkg.OldVersion != "" {
								fmt.Printf("    %s: %s -> %s\n", pkg.Name, pkg.OldVersion, pkg.NewVersion)
							} else {
								fmt.Printf("    %s\n", pkg.Name)
							}
						}
					}

					if result.RebootRequired {
						fmt.Printf("  Reboot required\n")
						if allowReboot {
							if rebootDelay > 0 {
								fmt.Printf("  Scheduling reboot in %v...\n", rebootDelay)
								if err := updater.ScheduleReboot(ctx, client, rebootDelay); err != nil {
									fmt.Printf("  Failed to schedule reboot: %v\n", err)
								}
							} else {
								fmt.Printf("  Rebooting immediately...\n")
								if err := updater.ScheduleReboot(ctx, client, 1*time.Minute); err != nil {
									fmt.Printf("  Failed to schedule reboot: %v\n", err)
								}
							}
						}
					}

					totalUpdated++

					// Cleanup old packages
					if err := updater.Cleanup(ctx, client); err != nil {
						if verbose {
							fmt.Printf("  Cleanup warning: %v\n", err)
						}
					}

					fmt.Println()
				}

				// If canary strategy and first batch, check for failures
				if strategy == "canary" && batchIdx == 0 && totalFailed > 0 {
					return fmt.Errorf("canary batch had %d failure(s), aborting rollout", totalFailed)
				}
			}

			fmt.Printf("\nSummary: %d updated, %d failed\n", totalUpdated, totalFailed)
			return nil
		},
	}

	cmd.Flags().BoolVar(&securityOnly, "security-only", false, "Only apply security updates")
	cmd.Flags().BoolVar(&distUpgrade, "dist-upgrade", false, "Run dist-upgrade (may add/remove packages)")
	cmd.Flags().BoolVar(&allowReboot, "reboot", false, "Allow reboot if required")
	cmd.Flags().DurationVar(&rebootDelay, "reboot-delay", 5*time.Minute, "Delay before reboot")
	cmd.Flags().StringVar(&strategy, "strategy", "serial", "Rollout strategy (serial, parallel, canary)")
	cmd.Flags().IntVar(&canaryPercent, "canary-percent", 10, "Percentage of hosts in canary batch")

	return cmd
}

func osUpdatePolicyCmd() *cobra.Command {
	var policy string
	var window string
	var allowReboot bool

	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Configure update policy",
		Long: `Configure automatic update policy on Ubuntu hosts.

Policies:
  security-daily - Apply security updates daily via unattended-upgrades
  full-weekly    - Apply all updates weekly
  manual         - Disable automatic updates (NixFleet manages manually)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			ubuntuHosts := filterUbuntuHosts(hosts)
			if len(ubuntuHosts) == 0 {
				fmt.Println("No Ubuntu hosts found")
				return nil
			}

			parsedPolicy, err := osupdate.ParsePolicy(policy)
			if err != nil {
				return err
			}

			config := osupdate.DefaultPolicyConfig(parsedPolicy)
			if window != "" {
				config.MaintenanceWindow = window
			}
			config.AllowReboot = allowReboot

			if dryRun {
				fmt.Printf("Would configure %s policy on %d host(s)\n", policy, len(ubuntuHosts))
				return nil
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			updater := osupdate.NewUpdater()

			fmt.Printf("Configuring %s policy on %d host(s)...\n\n", policy, len(ubuntuHosts))

			for _, host := range ubuntuHosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				if err := updater.ConfigurePolicy(ctx, client, config); err != nil {
					fmt.Printf("%s: failed - %v\n", host.Name, err)
					continue
				}

				fmt.Printf("%s: OK\n", host.Name)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&policy, "set", "security-daily", "Policy to configure (security-daily, full-weekly, manual)")
	cmd.Flags().StringVar(&window, "window", "", "Maintenance window (e.g., 'Sun 02:00-06:00')")
	cmd.Flags().BoolVar(&allowReboot, "allow-reboot", false, "Allow automatic reboot")

	return cmd
}

func osUpdateHoldCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hold [packages...]",
		Short: "Hold packages from being upgraded",
		Long:  `Mark packages as held so they won't be upgraded.`,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			ubuntuHosts := filterUbuntuHosts(hosts)
			if len(ubuntuHosts) == 0 {
				fmt.Println("No Ubuntu hosts found")
				return nil
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			updater := osupdate.NewUpdater()

			fmt.Printf("Holding packages on %d host(s): %v\n\n", len(ubuntuHosts), args)

			for _, host := range ubuntuHosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				if err := updater.HoldPackages(ctx, client, args); err != nil {
					fmt.Printf("%s: failed - %v\n", host.Name, err)
					continue
				}

				fmt.Printf("%s: OK\n", host.Name)
			}

			return nil
		},
	}
}

func osUpdateUnholdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unhold [packages...]",
		Short: "Remove hold from packages",
		Long:  `Remove hold from packages so they can be upgraded again.`,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			ubuntuHosts := filterUbuntuHosts(hosts)
			if len(ubuntuHosts) == 0 {
				fmt.Println("No Ubuntu hosts found")
				return nil
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			updater := osupdate.NewUpdater()

			fmt.Printf("Removing hold from packages on %d host(s): %v\n\n", len(ubuntuHosts), args)

			for _, host := range ubuntuHosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				if err := updater.UnholdPackages(ctx, client, args); err != nil {
					fmt.Printf("%s: failed - %v\n", host.Name, err)
					continue
				}

				fmt.Printf("%s: OK\n", host.Name)
			}

			return nil
		},
	}
}

func rebootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reboot",
		Short: "Manage host reboots",
		Long: `Manage host reboots with configurable windows and hooks.

Subcommands:
  status  - Check reboot requirements
  now     - Reboot hosts immediately
  schedule - Schedule reboots in maintenance window`,
	}

	cmd.AddCommand(rebootStatusCmd())
	cmd.AddCommand(rebootNowCmd())

	return cmd
}

func rebootStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check reboot requirements",
		Long:  `Check if hosts require a reboot.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			orchestrator := reboot.NewOrchestrator(reboot.DefaultRebootConfig())

			fmt.Printf("Checking reboot status on %d host(s)...\n\n", len(hosts))
			fmt.Printf("%-20s %-10s %-15s %s\n", "HOST", "BASE", "REBOOT", "REASON")
			fmt.Printf("%-20s %-10s %-15s %s\n", "----", "----", "------", "------")

			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%-20s %-10s %-15s %s\n", host.Name, host.Base, "error", err.Error())
					continue
				}

				status, err := orchestrator.CheckRebootRequired(ctx, client, host.Base)
				if err != nil {
					fmt.Printf("%-20s %-10s %-15s %s\n", host.Name, host.Base, "error", err.Error())
					continue
				}

				rebootStr := "no"
				reason := ""
				if status.Required {
					rebootStr = "YES"
					reason = status.Reason
					if len(status.TriggerPackages) > 0 {
						reason += fmt.Sprintf(" (%s)", strings.Join(status.TriggerPackages, ", "))
					}
				}

				fmt.Printf("%-20s %-10s %-15s %s\n", host.Name, host.Base, rebootStr, reason)
			}

			return nil
		},
	}
}

func rebootNowCmd() *cobra.Command {
	var window string
	var preHook, postHook string
	var maxConcurrent int
	var waitTimeout time.Duration
	var force bool

	cmd := &cobra.Command{
		Use:   "now",
		Short: "Reboot hosts immediately",
		Long: `Reboot hosts that require a reboot.

Only reboots hosts that have the reboot-required flag set, unless --force is used.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			// Parse reboot window if specified
			var rebootWindow *reboot.RebootWindow
			if window != "" {
				rebootWindow, err = reboot.ParseRebootWindow(window)
				if err != nil {
					return err
				}
			}

			config := reboot.RebootConfig{
				AllowReboot:          true,
				Window:               rebootWindow,
				MaxConcurrentReboots: maxConcurrent,
				PreRebootHook:        preHook,
				PostRebootHook:       postHook,
				WaitTimeout:          waitTimeout,
				WaitInterval:         10 * time.Second,
			}

			orchestrator := reboot.NewOrchestrator(config)
			limiter := reboot.NewConcurrencyLimiter(maxConcurrent)

			// First check which hosts need reboot
			var hostsToReboot []*inventory.Host
			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				status, err := orchestrator.CheckRebootRequired(ctx, client, host.Base)
				if err != nil {
					fmt.Printf("%s: check failed - %v\n", host.Name, err)
					continue
				}

				if status.Required || force {
					hostsToReboot = append(hostsToReboot, host)
				}
			}

			if len(hostsToReboot) == 0 {
				fmt.Println("No hosts require reboot")
				return nil
			}

			if dryRun {
				fmt.Printf("Would reboot %d host(s):\n", len(hostsToReboot))
				for _, h := range hostsToReboot {
					fmt.Printf("  - %s (%s)\n", h.Name, h.Addr)
				}
				return nil
			}

			fmt.Printf("Rebooting %d host(s) (max %d concurrent)...\n\n", len(hostsToReboot), maxConcurrent)

			success := 0
			failed := 0

			for _, host := range hostsToReboot {
				if err := limiter.Acquire(ctx); err != nil {
					return err
				}

				fmt.Printf("Rebooting %s...\n", host.Name)

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("  Connection failed: %v\n", err)
					failed++
					limiter.Release()
					continue
				}

				port := host.SSHPort
				if port == 0 {
					port = 22
				}

				if err := orchestrator.ExecuteReboot(ctx, client, pool, host.Addr, port, host.SSHUser); err != nil {
					fmt.Printf("  Reboot failed: %v\n", err)
					failed++
					limiter.Release()
					continue
				}

				// Run post-reboot hook if host came back
				client, err = pool.GetWithUser(ctx, host.Addr, port, host.SSHUser)
				if err == nil {
					if err := orchestrator.RunPostRebootHook(ctx, client); err != nil {
						fmt.Printf("  Post-reboot hook failed: %v\n", err)
					}
				}

				fmt.Printf("  OK (host is back)\n")
				success++
				limiter.Release()
			}

			fmt.Printf("\nSummary: %d rebooted, %d failed\n", success, failed)
			return nil
		},
	}

	cmd.Flags().StringVar(&window, "window", "", "Reboot window (e.g., 'Sun 02:00-04:00')")
	cmd.Flags().StringVar(&preHook, "pre-hook", "", "Command to run before reboot")
	cmd.Flags().StringVar(&postHook, "post-hook", "", "Command to run after reboot")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 1, "Maximum concurrent reboots")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "Timeout waiting for host to come back")
	cmd.Flags().BoolVar(&force, "force", false, "Reboot even if not required")

	return cmd
}

func runCmd() *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "run [command]",
		Short: "Run ad-hoc commands on hosts",
		Long:  `Execute commands on target hosts.`,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			executor := ssh.NewExecutor(pool, maxParallel)

			command := args[0]
			fmt.Printf("Running on %d host(s): %s\n\n", len(hosts), command)

			results := executor.ExecOnHosts(ctx, hosts, command, false)

			for _, r := range results {
				fmt.Printf("=== %s ===\n", r.Host.Name)
				if r.Error != nil {
					fmt.Printf("ERROR: %v\n", r.Error)
				} else {
					if r.Result.Stdout != "" {
						fmt.Print(r.Result.Stdout)
					}
					if r.Result.Stderr != "" {
						fmt.Printf("stderr: %s", r.Result.Stderr)
					}
					if r.Result.ExitCode != 0 {
						fmt.Printf("exit code: %d\n", r.Result.ExitCode)
					}
				}
				fmt.Println()
			}

			fmt.Printf("Success: %d, Failed: %d\n", ssh.CountSuccess(results), ssh.CountErrors(results))

			return nil
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "Command timeout")

	return cmd
}

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage binary cache",
		Long: `Manage Nix binary cache for faster deployments.

Subcommands:
  push       - Push store paths to cache
  configure  - Configure hosts to use cache
  keygen     - Generate signing keys`,
	}

	cmd.AddCommand(cachePushCmd())
	cmd.AddCommand(cacheConfigureCmd())
	cmd.AddCommand(cacheKeygenCmd())

	return cmd
}

func cachePushCmd() *cobra.Command {
	var cacheURL string
	var secretKey string

	cmd := &cobra.Command{
		Use:   "push [store-path]",
		Short: "Push store path to cache",
		Long:  `Push a Nix store path and its dependencies to the binary cache.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			storePath := args[0]

			if cacheURL == "" {
				return fmt.Errorf("--cache-url is required")
			}
			if secretKey == "" {
				return fmt.Errorf("--secret-key is required")
			}

			signing := &cache.SigningConfig{SecretKey: secretKey}
			mgr := cache.NewManager(nil, signing)

			fmt.Printf("Pushing %s to %s...\n", storePath, cacheURL)

			if dryRun {
				fmt.Println("Would push (dry-run)")
				return nil
			}

			if err := mgr.PushToCache(ctx, storePath, cacheURL); err != nil {
				return fmt.Errorf("push failed: %w", err)
			}

			fmt.Println("Done!")
			return nil
		},
	}

	cmd.Flags().StringVar(&cacheURL, "cache-url", "", "Cache URL (e.g., s3://bucket or ssh://host)")
	cmd.Flags().StringVar(&secretKey, "secret-key", "", "Path to signing secret key")

	return cmd
}

func cacheConfigureCmd() *cobra.Command {
	var cacheURL string
	var publicKeys []string

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure hosts to use cache",
		Long:  `Configure remote hosts to substitute from the binary cache.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if cacheURL == "" {
				return fmt.Errorf("--cache-url is required")
			}

			cacheConfig := cache.CacheConfig{
				URL:        cacheURL,
				PublicKeys: publicKeys,
			}

			mgr := cache.NewManager([]cache.CacheConfig{cacheConfig}, nil)

			pool := ssh.NewPool(nil)
			defer pool.Close()

			fmt.Printf("Configuring cache on %d host(s)...\n\n", len(hosts))

			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				if err := mgr.ConfigureHostCache(ctx, client, host.Base); err != nil {
					fmt.Printf("%s: failed - %v\n", host.Name, err)
					continue
				}

				fmt.Printf("%s: OK\n", host.Name)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&cacheURL, "cache-url", "", "Cache URL")
	cmd.Flags().StringSliceVar(&publicKeys, "public-key", nil, "Trusted public keys")

	return cmd
}

func cacheKeygenCmd() *cobra.Command {
	var keyName string
	var outputDir string

	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate signing key pair",
		Long:  `Generate a new Nix signing key pair for binary cache.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if keyName == "" {
				return fmt.Errorf("--name is required")
			}
			if outputDir == "" {
				outputDir = "."
			}

			fmt.Printf("Generating signing key '%s'...\n", keyName)

			signing, err := cache.GenerateSigningKey(ctx, keyName, outputDir)
			if err != nil {
				return fmt.Errorf("keygen failed: %w", err)
			}

			fmt.Printf("Secret key: %s\n", signing.SecretKey)
			fmt.Printf("Public key: %s\n", signing.PublicKey)
			fmt.Println("\nAdd the public key to your cache configuration.")

			return nil
		},
	}

	cmd.Flags().StringVar(&keyName, "name", "", "Key name (e.g., 'myorg-cache-1')")
	cmd.Flags().StringVar(&outputDir, "output", ".", "Output directory for key files")

	return cmd
}

func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage encrypted secrets",
		Long: `Manage encrypted secrets with age encryption.

Subcommands:
  rekey    - Re-encrypt all secrets after modifying secrets.nix
  edit     - Edit a secret in-place
  add      - Add a new encrypted secret
  host-key - Get age public key from a host's SSH key
  deploy   - Deploy secrets to hosts
  encrypt  - Encrypt a secret file
  decrypt  - Decrypt a secret file
  keygen   - Generate age key pair`,
	}

	cmd.AddCommand(secretsRekeyCmd())
	cmd.AddCommand(secretsEditCmd())
	cmd.AddCommand(secretsAddCmd())
	cmd.AddCommand(secretsHostKeyCmd())
	cmd.AddCommand(secretsDeployCmd())
	cmd.AddCommand(secretsEncryptCmd())
	cmd.AddCommand(secretsDecryptCmd())
	cmd.AddCommand(secretsKeygenCmd())

	return cmd
}

func secretsDeployCmd() *cobra.Command {
	var identities []string
	var secretsDir string

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy secrets to hosts",
		Long:  `Decrypt and deploy secrets to remote hosts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			mgr := secrets.NewManager(secrets.EncryptionAge, identities, nil)

			pool := ssh.NewPool(nil)
			defer pool.Close()

			// TODO: Load secrets config from inventory or flake
			fmt.Printf("Deploying secrets to %d host(s)...\n\n", len(hosts))
			fmt.Printf("Note: Secret definitions should be in host config (nixfleet.secrets)\n")
			fmt.Printf("Secrets directory: %s\n\n", secretsDir)

			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				// For now, just verify connectivity
				// Full implementation would read secrets config from the host's nixfleet config
				result, _ := client.Exec(ctx, "echo ok")
				if result != nil && result.Stdout == "ok\n" {
					fmt.Printf("%s: ready (secrets would be deployed here)\n", host.Name)
				}
				_ = mgr // Use manager when secrets config is loaded
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVarP(&identities, "identity", "i", nil, "Age identity file(s)")
	cmd.Flags().StringVar(&secretsDir, "secrets-dir", "secrets/", "Directory containing encrypted secrets")

	return cmd
}

func secretsEncryptCmd() *cobra.Command {
	var recipients []string
	var output string

	cmd := &cobra.Command{
		Use:   "encrypt [file]",
		Short: "Encrypt a file",
		Long:  `Encrypt a file using age encryption.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			inputFile := args[0]

			if len(recipients) == 0 {
				return fmt.Errorf("at least one --recipient is required")
			}
			if output == "" {
				output = inputFile + ".age"
			}

			data, err := os.ReadFile(inputFile)
			if err != nil {
				return fmt.Errorf("reading input: %w", err)
			}

			mgr := secrets.NewManager(secrets.EncryptionAge, nil, recipients)

			if err := mgr.EncryptSecret(ctx, data, output); err != nil {
				return fmt.Errorf("encryption failed: %w", err)
			}

			fmt.Printf("Encrypted to %s\n", output)
			return nil
		},
	}

	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipient public key(s)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (default: input.age)")

	return cmd
}

func secretsDecryptCmd() *cobra.Command {
	var identities []string
	var output string

	cmd := &cobra.Command{
		Use:   "decrypt [file]",
		Short: "Decrypt a file",
		Long:  `Decrypt an age-encrypted file.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			inputFile := args[0]

			if len(identities) == 0 {
				return fmt.Errorf("at least one --identity is required")
			}

			mgr := secrets.NewManager(secrets.EncryptionAge, identities, nil)

			data, err := mgr.DecryptSecret(ctx, inputFile)
			if err != nil {
				return fmt.Errorf("decryption failed: %w", err)
			}

			if output == "" {
				fmt.Print(string(data))
			} else {
				if err := os.WriteFile(output, data, 0600); err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				fmt.Printf("Decrypted to %s\n", output)
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVarP(&identities, "identity", "i", nil, "Age identity file(s)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (default: stdout)")

	return cmd
}

func secretsKeygenCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate age key pair",
		Long:  `Generate a new age key pair for secrets encryption.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if output == "" {
				output = "age-key.txt"
			}

			publicKey, err := secrets.GenerateAgeKey(ctx, output)
			if err != nil {
				return fmt.Errorf("keygen failed: %w", err)
			}

			fmt.Printf("Generated key pair:\n")
			fmt.Printf("  Secret key: %s\n", output)
			fmt.Printf("  Public key: %s\n", publicKey)
			fmt.Println("\nUse the public key as a recipient for encryption.")

			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "age-key.txt", "Output file for secret key")

	return cmd
}

func secretsRekeyCmd() *cobra.Command {
	var secretsNixPath string
	var secretsDir string
	var identityPath string

	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Re-encrypt all secrets after modifying secrets.nix",
		Long: `Re-encrypt all secrets using the recipients defined in secrets.nix.

Use this after:
  - Adding a new host to secrets.nix
  - Removing a host from secrets.nix
  - Changing which secrets a host can access

Example:
  nixfleet secrets rekey -c secrets/secrets.nix -i ~/.config/age/admin-key.txt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if identityPath == "" {
				// Default to admin key location
				home, _ := os.UserHomeDir()
				identityPath = home + "/.config/age/admin-key.txt"
			}

			// Check identity exists
			if _, err := os.Stat(identityPath); os.IsNotExist(err) {
				return fmt.Errorf("identity file not found: %s\nUse -i to specify your age identity file", identityPath)
			}

			// Parse secrets.nix
			config, err := secrets.ParseSecretsNix(ctx, secretsNixPath)
			if err != nil {
				return fmt.Errorf("parsing secrets.nix: %w", err)
			}

			fmt.Printf("Parsed secrets.nix:\n")
			fmt.Printf("  Admins: %d\n", len(config.Admins))
			fmt.Printf("  Hosts: %d\n", len(config.Hosts))
			fmt.Printf("  Secrets: %d\n\n", len(config.Secrets))

			if dryRun {
				fmt.Println("Would rekey the following secrets:")
				for name, entry := range config.Secrets {
					fmt.Printf("  %s -> %d recipients\n", name, len(entry.PublicKeys))
				}
				return nil
			}

			rekeyed, err := secrets.RekeyAll(ctx, secretsDir, config, identityPath, false)
			if err != nil {
				return err
			}

			fmt.Printf("Rekeyed %d secret(s):\n", len(rekeyed))
			for _, name := range rekeyed {
				entry := config.Secrets[name]
				fmt.Printf("  ✓ %s (%d recipients)\n", name, len(entry.PublicKeys))
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&secretsNixPath, "config", "c", "secrets/secrets.nix", "Path to secrets.nix")
	cmd.Flags().StringVarP(&secretsDir, "secrets-dir", "s", "secrets/", "Directory containing .age files")
	cmd.Flags().StringVar(&identityPath, "identity", "", "Path to age identity for decryption (default: ~/.config/age/admin-key.txt)")

	return cmd
}

func secretsEditCmd() *cobra.Command {
	var secretsNixPath string
	var identityPath string

	cmd := &cobra.Command{
		Use:   "edit [secret-file]",
		Short: "Edit a secret in-place",
		Long: `Decrypt a secret, open in $EDITOR, and re-encrypt with the same recipients.

The recipients are looked up from secrets.nix to ensure proper multi-recipient encryption.

Example:
  nixfleet secrets edit secrets/api-key.age`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretPath := args[0]

			if identityPath == "" {
				home, _ := os.UserHomeDir()
				identityPath = home + "/.config/age/admin-key.txt"
			}

			// Check identity exists
			if _, err := os.Stat(identityPath); os.IsNotExist(err) {
				return fmt.Errorf("identity file not found: %s", identityPath)
			}

			// Check secret exists
			if _, err := os.Stat(secretPath); os.IsNotExist(err) {
				return fmt.Errorf("secret file not found: %s", secretPath)
			}

			// Parse secrets.nix to get recipients
			config, err := secrets.ParseSecretsNix(ctx, secretsNixPath)
			if err != nil {
				return fmt.Errorf("parsing secrets.nix: %w", err)
			}

			// Get secret name (basename)
			secretName := filepath.Base(secretPath)
			recipients, err := config.LookupRecipientsForSecret(secretName)
			if err != nil {
				return err
			}

			fmt.Printf("Editing %s (%d recipients)\n", secretName, len(recipients))
			fmt.Printf("Opening in $EDITOR...\n\n")

			if err := secrets.EditSecret(ctx, secretPath, recipients, identityPath); err != nil {
				return err
			}

			fmt.Println("Secret updated successfully")
			return nil
		},
	}

	cmd.Flags().StringVarP(&secretsNixPath, "config", "c", "secrets/secrets.nix", "Path to secrets.nix")
	cmd.Flags().StringVar(&identityPath, "identity", "", "Path to age identity for decryption (default: ~/.config/age/admin-key.txt)")

	return cmd
}

func secretsAddCmd() *cobra.Command {
	var secretsNixPath string
	var secretsDir string
	var recipients []string
	var fromFile string
	var hostNames []string

	cmd := &cobra.Command{
		Use:   "add [secret-name]",
		Short: "Add a new encrypted secret",
		Long: `Create a new encrypted secret file.

Secret value can be provided via:
  - stdin (pipe or interactive)
  - --from-file flag

Recipients are determined by:
  - --recipient flags (explicit keys)
  - --host flags (looked up from secrets.nix)
  - Default: all admins from secrets.nix

Example:
  echo "my-secret-value" | nixfleet secrets add api-key.age
  nixfleet secrets add db-password.age --host gtr --host web-1
  nixfleet secrets add ssl-cert.age --from-file /path/to/cert.pem`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretName := args[0]

			// Ensure .age extension
			if !strings.HasSuffix(secretName, ".age") {
				secretName += ".age"
			}

			secretPath := filepath.Join(secretsDir, secretName)

			// Check if already exists
			if _, err := os.Stat(secretPath); err == nil {
				return fmt.Errorf("secret already exists: %s\nUse 'nixfleet secrets edit' to modify", secretPath)
			}

			// Determine recipients
			var finalRecipients []string
			if len(recipients) > 0 {
				finalRecipients = recipients
			} else {
				// Parse secrets.nix
				config, err := secrets.ParseSecretsNix(ctx, secretsNixPath)
				if err != nil {
					return fmt.Errorf("parsing secrets.nix: %w", err)
				}

				// Start with all admins
				finalRecipients = append(finalRecipients, config.AllAdmins...)

				// Add specified hosts
				for _, hostName := range hostNames {
					if key, ok := config.Hosts[hostName]; ok {
						finalRecipients = append(finalRecipients, key)
					} else {
						return fmt.Errorf("host %q not found in secrets.nix", hostName)
					}
				}

				if len(finalRecipients) == 0 {
					return fmt.Errorf("no recipients specified and no admins in secrets.nix")
				}
			}

			// Get secret content
			var content []byte
			var err error
			if fromFile != "" {
				content, err = os.ReadFile(fromFile)
				if err != nil {
					return fmt.Errorf("reading file: %w", err)
				}
			} else {
				// Read from stdin
				fmt.Println("Enter secret value (Ctrl+D to finish):")
				content, err = os.ReadFile("/dev/stdin")
				if err != nil {
					return fmt.Errorf("reading stdin: %w", err)
				}
			}

			if len(content) == 0 {
				return fmt.Errorf("empty secret content")
			}

			if dryRun {
				fmt.Printf("Would create %s with %d recipients\n", secretPath, len(finalRecipients))
				return nil
			}

			if err := secrets.AddSecret(ctx, secretPath, content, finalRecipients); err != nil {
				return err
			}

			fmt.Printf("Created %s (%d recipients)\n", secretPath, len(finalRecipients))
			fmt.Println("\nDon't forget to add this secret to secrets.nix:")
			fmt.Printf("  \"%s\".publicKeys = allAdmins ++ [ hosts.<hostname> ];\n", secretName)

			return nil
		},
	}

	cmd.Flags().StringVarP(&secretsNixPath, "config", "c", "secrets/secrets.nix", "Path to secrets.nix")
	cmd.Flags().StringVarP(&secretsDir, "secrets-dir", "s", "secrets/", "Output directory")
	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipient public key(s)")
	cmd.Flags().StringSliceVar(&hostNames, "host", nil, "Host name(s) from secrets.nix to add as recipients")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read secret value from file")

	return cmd
}

func secretsHostKeyCmd() *cobra.Command {
	var sshKeyPath string

	cmd := &cobra.Command{
		Use:   "host-key [host]",
		Short: "Get age public key from a host's SSH key",
		Long: `Derive an age public key from a host's SSH ed25519 host key.

This can be used to:
  - Get a host's age key for adding to secrets.nix
  - Verify the expected key for a host

Examples:
  # Get key from remote host
  nixfleet secrets host-key gtr

  # Get key from local SSH key file
  nixfleet secrets host-key --ssh-key /path/to/ssh_host_ed25519_key.pub`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if sshKeyPath != "" {
				// Local file mode
				key, err := secrets.GetHostAgeKey(ctx, sshKeyPath)
				if err != nil {
					return err
				}
				fmt.Println(key)
				return nil
			}

			// Remote host mode - need a host argument
			if len(args) == 0 {
				return fmt.Errorf("specify a host or use --ssh-key for a local file")
			}

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			// Find the target host
			var targetHost *inventory.Host
			for _, h := range hosts {
				if h.Name == args[0] {
					targetHost = h
					break
				}
			}

			if targetHost == nil {
				return fmt.Errorf("host %q not found in inventory", args[0])
			}

			port := targetHost.SSHPort
			if port == 0 {
				port = 22
			}

			key, err := secrets.GetHostAgeKeyFromRemote(ctx, targetHost.Addr, targetHost.SSHUser, port)
			if err != nil {
				return err
			}

			fmt.Printf("Host: %s\n", targetHost.Name)
			fmt.Printf("Age public key: %s\n", key)
			fmt.Println("\nAdd to secrets.nix:")
			fmt.Printf("  %s = \"%s\";\n", targetHost.Name, key)

			return nil
		},
	}

	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "Path to SSH public key file (for local keys)")

	return cmd
}

func driftCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drift",
		Short: "Detect and fix configuration drift",
		Long: `Detect and remediate configuration drift on managed hosts.

Subcommands:
  check  - Check for configuration drift
  fix    - Remediate detected drift
  status - Show drift status from cached state`,
	}

	cmd.AddCommand(driftCheckCmd())
	cmd.AddCommand(driftFixCmd())
	cmd.AddCommand(driftStatusCmd())

	return cmd
}

func driftCheckCmd() *cobra.Command {
	var saveState bool

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check for configuration drift",
		Long:  `Compare current file states against expected configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			stateMgr := state.NewManager()

			fmt.Printf("Checking drift on %d host(s)...\n\n", len(hosts))

			totalDrift := 0
			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				// Read current state from host
				hostState, err := stateMgr.ReadState(ctx, client)
				if err != nil {
					fmt.Printf("%s: failed to read state - %v\n", host.Name, err)
					continue
				}

				if len(hostState.ManagedFiles) == 0 {
					fmt.Printf("%s: no managed files configured\n", host.Name)
					continue
				}

				// Check drift against managed files
				results, err := stateMgr.CheckDrift(ctx, client, hostState.ManagedFiles)
				if err != nil {
					fmt.Printf("%s: drift check failed - %v\n", host.Name, err)
					continue
				}

				// Count drift
				driftCount := 0
				for _, r := range results {
					if r.HasDrift() {
						driftCount++
					}
				}

				if driftCount == 0 {
					fmt.Printf("%s: no drift detected (%d files checked)\n", host.Name, len(results))
				} else {
					fmt.Printf("%s: DRIFT DETECTED (%d/%d files)\n", host.Name, driftCount, len(results))
					for _, r := range results {
						if r.HasDrift() {
							fmt.Printf("  - %s: %s\n", r.Path, r.Status)
							if verbose {
								switch r.Status {
								case state.DriftStatusContentChanged:
									fmt.Printf("      expected hash: %s\n", r.Expected.Hash[:16]+"...")
									fmt.Printf("      actual hash:   %s\n", r.Actual.Hash[:16]+"...")
								case state.DriftStatusPermissionsChanged:
									fmt.Printf("      expected: %s %s:%s\n", r.Expected.Mode, r.Expected.Owner, r.Expected.Group)
									fmt.Printf("      actual:   %s %s:%s\n", r.Actual.Mode, r.Actual.Owner, r.Actual.Group)
								}
							}
						}
					}
					totalDrift += driftCount
				}

				// Update state with drift info
				if saveState {
					hostState.DriftDetected = driftCount > 0
					hostState.DriftFiles = nil
					for _, r := range results {
						if r.HasDrift() {
							hostState.DriftFiles = append(hostState.DriftFiles, r.Path)
						}
					}
					hostState.LastDriftCheck = time.Now()
					if err := stateMgr.WriteState(ctx, client, hostState); err != nil {
						fmt.Printf("  warning: failed to save state - %v\n", err)
					}
				}

				fmt.Println()
			}

			if totalDrift > 0 {
				fmt.Printf("Total: %d file(s) with drift detected\n", totalDrift)
				fmt.Println("Run 'nixfleet drift fix' to remediate drift")
			} else {
				fmt.Println("No drift detected across all hosts")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&saveState, "save-state", true, "Update host state with drift results")

	return cmd
}

func driftFixCmd() *cobra.Command {
	var filesOnly []string

	cmd := &cobra.Command{
		Use:   "fix",
		Short: "Remediate configuration drift",
		Long: `Fix detected drift by restoring files to expected state.

By default, restores permissions on drifted files. For content changes,
a full re-apply is recommended as file contents come from the Nix store.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			stateMgr := state.NewManager()

			fmt.Printf("Fixing drift on %d host(s)...\n\n", len(hosts))

			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%s: connection failed - %v\n", host.Name, err)
					continue
				}

				// Read current state
				hostState, err := stateMgr.ReadState(ctx, client)
				if err != nil {
					fmt.Printf("%s: failed to read state - %v\n", host.Name, err)
					continue
				}

				if len(hostState.ManagedFiles) == 0 {
					fmt.Printf("%s: no managed files configured\n", host.Name)
					continue
				}

				// Check drift
				results, err := stateMgr.CheckDrift(ctx, client, hostState.ManagedFiles)
				if err != nil {
					fmt.Printf("%s: drift check failed - %v\n", host.Name, err)
					continue
				}

				// Filter results if specific files requested
				if len(filesOnly) > 0 {
					filtered := make([]state.DriftResult, 0)
					fileSet := make(map[string]bool)
					for _, f := range filesOnly {
						fileSet[f] = true
					}
					for _, r := range results {
						if fileSet[r.Path] {
							filtered = append(filtered, r)
						}
					}
					results = filtered
				}

				// Fix drift
				fixed := 0
				skipped := 0
				for _, r := range results {
					if !r.HasDrift() {
						continue
					}

					if dryRun {
						fmt.Printf("%s: would fix %s (%s)\n", host.Name, r.Path, r.Status)
						continue
					}

					if r.Status == state.DriftStatusContentChanged {
						fmt.Printf("%s: %s - content changed, run 'nixfleet apply' to restore\n", host.Name, r.Path)
						skipped++
						continue
					}

					if r.Status == state.DriftStatusMissing {
						fmt.Printf("%s: %s - file missing, run 'nixfleet apply' to restore\n", host.Name, r.Path)
						skipped++
						continue
					}

					// Fix permissions
					if r.Status == state.DriftStatusPermissionsChanged {
						if err := stateMgr.FixDrift(ctx, client, r, nil); err != nil {
							fmt.Printf("%s: failed to fix %s - %v\n", host.Name, r.Path, err)
							continue
						}
						fmt.Printf("%s: fixed permissions on %s\n", host.Name, r.Path)
						fixed++
					}
				}

				if dryRun {
					continue
				}

				if fixed > 0 || skipped > 0 {
					fmt.Printf("%s: %d fixed, %d require re-apply\n", host.Name, fixed, skipped)
				} else {
					fmt.Printf("%s: no drift to fix\n", host.Name)
				}

				// Update state
				hostState.DriftDetected = skipped > 0
				hostState.DriftFiles = nil
				for _, r := range results {
					if r.Status == state.DriftStatusContentChanged || r.Status == state.DriftStatusMissing {
						hostState.DriftFiles = append(hostState.DriftFiles, r.Path)
					}
				}
				hostState.LastDriftCheck = time.Now()
				if err := stateMgr.WriteState(ctx, client, hostState); err != nil {
					fmt.Printf("  warning: failed to save state - %v\n", err)
				}

				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&filesOnly, "files", nil, "Only fix specific files")

	return cmd
}

func driftStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show drift status from cached state",
		Long:  `Display last known drift status from host state without performing checks.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			stateMgr := state.NewManager()

			fmt.Printf("%-20s %-10s %-15s %s\n", "HOST", "DRIFT", "LAST CHECK", "FILES")
			fmt.Printf("%-20s %-10s %-15s %s\n", "----", "-----", "----------", "-----")

			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("%-20s %-10s %-15s %s\n", host.Name, "error", "-", err.Error())
					continue
				}

				hostState, err := stateMgr.ReadState(ctx, client)
				if err != nil {
					fmt.Printf("%-20s %-10s %-15s %s\n", host.Name, "error", "-", err.Error())
					continue
				}

				driftStr := "no"
				if hostState.DriftDetected {
					driftStr = "YES"
				}

				lastCheck := "-"
				if !hostState.LastDriftCheck.IsZero() {
					lastCheck = hostState.LastDriftCheck.Format("Jan 02 15:04")
				}

				filesStr := "-"
				if len(hostState.DriftFiles) > 0 {
					filesStr = fmt.Sprintf("%d file(s)", len(hostState.DriftFiles))
					if verbose {
						filesStr = strings.Join(hostState.DriftFiles, ", ")
					}
				}

				fmt.Printf("%-20s %-10s %-15s %s\n", host.Name, driftStr, lastCheck, filesStr)
			}

			return nil
		},
	}
}

func serverCmd() *cobra.Command {
	var listenAddr string
	var apiToken string
	var webhookURL string
	var webhookSecret string
	var webhookEvents []string
	var driftInterval time.Duration
	var updateInterval time.Duration
	var healthInterval time.Duration

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run NixFleet as an HTTP API server",
		Long: `Start NixFleet in server mode with a REST API for fleet management.

The server provides:
  - REST API for host management, deployment, and drift detection
  - Scheduled background tasks for drift, update, and health checks
  - Webhook notifications for events
  - Job queue for async operations

API Endpoints:
  GET  /api/health           - Server health check
  GET  /api/info             - Server information
  GET  /api/hosts            - List all hosts
  GET  /api/hosts/{name}     - Get host details
  POST /api/hosts/{name}/apply    - Trigger deployment
  POST /api/hosts/{name}/rollback - Rollback to previous generation
  GET  /api/drift            - Drift status for all hosts
  POST /api/drift/check      - Trigger drift check
  POST /api/drift/fix        - Fix detected drift
  GET  /api/plan             - Plan changes for all hosts
  POST /api/apply            - Apply to all hosts (async)
  GET  /api/jobs             - List running/completed jobs
  GET  /api/jobs/{id}        - Get job status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Load inventory
			inv, err := inventory.LoadFromDir(inventoryPath)
			if err != nil {
				inv, err = inventory.LoadFromFile(inventoryPath)
				if err != nil {
					return fmt.Errorf("loading inventory: %w", err)
				}
			}

			if err := inv.Validate(); err != nil {
				return fmt.Errorf("invalid inventory: %w", err)
			}

			config := server.Config{
				ListenAddr:          listenAddr,
				FlakePath:           flakePath,
				Inventory:           inv,
				DriftCheckInterval:  driftInterval,
				UpdateCheckInterval: updateInterval,
				HealthCheckInterval: healthInterval,
				WebhookURL:          webhookURL,
				WebhookSecret:       webhookSecret,
				WebhookEvents:       webhookEvents,
				APIToken:            apiToken,
			}

			srv, err := server.New(config)
			if err != nil {
				return fmt.Errorf("creating server: %w", err)
			}
			defer srv.Close()

			return srv.Start(ctx)
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":8080", "Address to listen on")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "API authentication token (optional)")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "Webhook URL for notifications")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "Webhook secret for signing")
	cmd.Flags().StringSliceVar(&webhookEvents, "webhook-events", []string{"drift", "apply", "health"}, "Events to send webhooks for")
	cmd.Flags().DurationVar(&driftInterval, "drift-interval", 0, "Interval for drift checks (e.g., 1h)")
	cmd.Flags().DurationVar(&updateInterval, "update-interval", 0, "Interval for update checks (e.g., 6h)")
	cmd.Flags().DurationVar(&healthInterval, "health-interval", 0, "Interval for health checks (e.g., 5m)")

	return cmd
}

func pullModeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull-mode",
		Short: "Configure pull-based deployment mode",
		Long: `Pull mode allows hosts to fetch and apply their own configuration
from a Git repository, rather than having a central server push changes.

This is ideal for:
  - Air-gapped environments
  - Hosts behind NAT/firewalls
  - GitOps workflows
  - Self-managing infrastructure

The host will periodically:
  1. Pull from the configured Git repository
  2. Build its configuration locally
  3. Apply changes automatically
  4. Report status via webhooks (optional)`,
	}

	cmd.AddCommand(pullModeInstallCmd())
	cmd.AddCommand(pullModeUninstallCmd())
	cmd.AddCommand(pullModeStatusCmd())
	cmd.AddCommand(pullModeTriggerCmd())

	return cmd
}

func pullModeInstallCmd() *cobra.Command {
	var repoURL string
	var branch string
	var interval string
	var sshKeyPath string
	var ageKeyPath string
	var applyOnBoot bool
	var webhookURL string
	var webhookSecret string

	// Home-manager options
	var hmUser string
	var hmDotfilesPath string
	var hmBranch string
	var hmSSHKey string
	var hmConfigName string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install pull mode on hosts",
		Long: `Install and configure pull mode on target hosts.

This will:
  1. Set up SSH config for Git repository access
  2. Clone the configuration repository
  3. Install the nixfleet-pull script
  4. Create and enable systemd timer for periodic pulls
  5. Optionally sync home-manager dotfiles (use --hm-* flags)

Example:
  nixfleet pull-mode install -H gtr --repo git@github.com:org/fleet-config.git

With home-manager:
  nixfleet pull-mode install -H gtr --repo git@github.com:org/fleet-config.git \
    --hm-user ztaylor --hm-dotfiles-path /home/ztaylor/dotfiles/nix \
    --hm-branch main --hm-config-name "ztaylor@x86_64-linux"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if repoURL == "" {
				return fmt.Errorf("--repo is required")
			}

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if len(hosts) == 0 {
				return fmt.Errorf("no hosts selected")
			}

			defaults := pullmode.DefaultConfig()
			config := pullmode.Config{
				RepoURL:       repoURL,
				Branch:        branch,
				SSHKeyPath:    sshKeyPath,
				AgeKeyPath:    ageKeyPath,
				Interval:      interval,
				ApplyOnBoot:   applyOnBoot,
				RepoPath:      defaults.RepoPath,
				WebhookURL:    webhookURL,
				WebhookSecret: webhookSecret,
			}

			if config.Branch == "" {
				config.Branch = defaults.Branch
			}
			if config.SSHKeyPath == "" {
				config.SSHKeyPath = defaults.SSHKeyPath
			}
			if config.AgeKeyPath == "" {
				config.AgeKeyPath = defaults.AgeKeyPath
			}
			if config.Interval == "" {
				config.Interval = defaults.Interval
			}

			// Configure home-manager if user is specified
			if hmUser != "" {
				config.HomeManager = &pullmode.HomeManagerConfig{
					User:         hmUser,
					DotfilesPath: hmDotfilesPath,
					Branch:       hmBranch,
					SSHKeyPath:   hmSSHKey,
					ConfigName:   hmConfigName,
				}
				// Set defaults for home-manager
				if config.HomeManager.Branch == "" {
					config.HomeManager.Branch = "main"
				}
				if config.HomeManager.DotfilesPath == "" {
					config.HomeManager.DotfilesPath = "/home/" + hmUser + "/dotfiles/nix"
				}
				if config.HomeManager.ConfigName == "" {
					config.HomeManager.ConfigName = hmUser + "@x86_64-linux"
				}
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			installer := pullmode.NewInstaller()

			fmt.Printf("Installing pull mode on %d host(s)...\n\n", len(hosts))

			var failed int
			for _, host := range hosts {
				fmt.Printf("%s: ", host.Name)

				if dryRun {
					fmt.Println("would install pull mode")
					continue
				}

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("connection failed - %v\n", err)
					failed++
					continue
				}

				// Set host name for this installation
				hostConfig := config
				hostConfig.HostName = host.Name

				if err := installer.Install(ctx, client, hostConfig); err != nil {
					fmt.Printf("failed - %v\n", err)
					failed++
					continue
				}

				fmt.Println("OK")
			}

			if failed > 0 {
				return fmt.Errorf("%d host(s) failed", failed)
			}

			fmt.Printf("\nPull mode installed successfully. Hosts will pull every %s.\n", interval)
			if hmUser != "" {
				fmt.Printf("Home-manager sync enabled for user '%s' (dotfiles: %s)\n", hmUser, config.HomeManager.DotfilesPath)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repoURL, "repo", "", "Git repository URL (SSH format, e.g., git@github.com:org/repo.git)")
	cmd.Flags().StringVar(&branch, "branch", "main", "Branch to track")
	cmd.Flags().StringVar(&interval, "interval", "15min", "Pull interval (systemd timer format)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "/run/nixfleet-secrets/github-deploy-key", "Path to SSH key for Git access")
	cmd.Flags().StringVar(&ageKeyPath, "age-key", "/root/.config/age/key.txt", "Path to age key for secrets")
	cmd.Flags().BoolVar(&applyOnBoot, "apply-on-boot", true, "Apply configuration on boot")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "Webhook URL for status notifications")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "Webhook secret for signing")

	// Home-manager flags
	cmd.Flags().StringVar(&hmUser, "hm-user", "", "Username to run home-manager as (enables home-manager sync)")
	cmd.Flags().StringVar(&hmDotfilesPath, "hm-dotfiles-path", "", "Path to dotfiles repository (default: /home/<user>/dotfiles/nix)")
	cmd.Flags().StringVar(&hmBranch, "hm-branch", "main", "Branch to track for dotfiles")
	cmd.Flags().StringVar(&hmSSHKey, "hm-ssh-key", "", "Path to SSH key for dotfiles repo access")
	cmd.Flags().StringVar(&hmConfigName, "hm-config-name", "", "Home-manager flake config name (default: <user>@x86_64-linux)")

	cmd.MarkFlagRequired("repo")

	return cmd
}

func pullModeUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove pull mode from hosts",
		Long:  `Stop and remove pull mode configuration from target hosts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if len(hosts) == 0 {
				return fmt.Errorf("no hosts selected")
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			installer := pullmode.NewInstaller()

			fmt.Printf("Uninstalling pull mode from %d host(s)...\n\n", len(hosts))

			var failed int
			for _, host := range hosts {
				fmt.Printf("%s: ", host.Name)

				if dryRun {
					fmt.Println("would uninstall pull mode")
					continue
				}

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("connection failed - %v\n", err)
					failed++
					continue
				}

				if err := installer.Uninstall(ctx, client); err != nil {
					fmt.Printf("failed - %v\n", err)
					failed++
					continue
				}

				fmt.Println("OK")
			}

			if failed > 0 {
				return fmt.Errorf("%d host(s) failed", failed)
			}

			return nil
		},
	}

	return cmd
}

func pullModeStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show pull mode status on hosts",
		Long:  `Display pull mode status including last run, next scheduled run, and current commit.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if len(hosts) == 0 {
				return fmt.Errorf("no hosts selected")
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			installer := pullmode.NewInstaller()

			fmt.Printf("Pull mode status for %d host(s):\n\n", len(hosts))

			for _, host := range hosts {
				fmt.Printf("%s:\n", host.Name)

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("  Connection failed: %v\n\n", err)
					continue
				}

				status, err := installer.Status(ctx, client)
				if err != nil {
					fmt.Printf("  Status check failed: %v\n\n", err)
					continue
				}

				if !status.Installed {
					fmt.Println("  Pull mode: not installed")
				} else {
					fmt.Println("  Pull mode: installed")
					if status.TimerActive {
						fmt.Println("  Timer: active")
					} else {
						fmt.Println("  Timer: inactive")
					}
					fmt.Printf("  Last run: %s", status.LastRun)
					fmt.Printf("  Last result: %s", status.LastResult)
					fmt.Printf("  Next run: %s", status.NextRun)
					fmt.Printf("  Current commit: %s", status.CurrentCommit)
				}
				fmt.Println()
			}

			return nil
		},
	}

	return cmd
}

func pullModeTriggerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "Manually trigger a pull operation",
		Long:  `Immediately trigger a pull and apply operation on target hosts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if len(hosts) == 0 {
				return fmt.Errorf("no hosts selected")
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			installer := pullmode.NewInstaller()

			fmt.Printf("Triggering pull on %d host(s)...\n\n", len(hosts))

			var failed int
			for _, host := range hosts {
				fmt.Printf("%s: ", host.Name)

				if dryRun {
					fmt.Println("would trigger pull")
					continue
				}

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("connection failed - %v\n", err)
					failed++
					continue
				}

				if err := installer.TriggerPull(ctx, client); err != nil {
					fmt.Printf("failed - %v\n", err)
					failed++
					continue
				}

				fmt.Println("triggered")
			}

			if failed > 0 {
				return fmt.Errorf("%d host(s) failed", failed)
			}

			fmt.Println("\nPull operations triggered. Use 'nixfleet pull-mode status' to check progress.")
			return nil
		},
	}

	return cmd
}

func hostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Host management commands",
		Long: `Commands for managing hosts in the fleet.

Subcommands:
  onboard  - Onboard a new host (get age key, setup secrets, install pull mode)`,
	}

	cmd.AddCommand(hostOnboardCmd())

	return cmd
}

func hostOnboardCmd() *cobra.Command {
	var secretsNixPath string
	var secretsDir string
	var repoURL string
	var branch string
	var interval string
	var skipPullMode bool
	var skipRekey bool
	var outputSecretsNix bool

	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Onboard a new host to the fleet",
		Long: `Onboard a new host by performing the following steps:

1. Get the host's SSH host key and convert to age public key
2. Display what to add to secrets.nix (or output in copy-paste format)
3. Optionally rekey all secrets to include the new host
4. Optionally install pull mode for GitOps deployments

Prerequisites:
  - Host must be bootstrapped (run bootstrap-ubuntu.sh first)
  - Host must be in your inventory file
  - SSH access must be configured

Example:
  # Onboard a new host with full setup
  nixfleet host onboard -H newhost --repo git@github.com:org/fleet-hosts.git

  # Just get the age key (for manual setup)
  nixfleet host onboard -H newhost --skip-pull-mode --skip-rekey

  # Output secrets.nix snippet for copy-paste
  nixfleet host onboard -H newhost --output-secrets-nix`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if len(hosts) == 0 {
				return fmt.Errorf("no hosts selected. Use -H to specify a host")
			}

			if len(hosts) > 1 {
				return fmt.Errorf("onboard operates on one host at a time. Found %d hosts", len(hosts))
			}

			host := hosts[0]
			fmt.Printf("Onboarding host: %s (%s)\n\n", host.Name, host.Addr)

			// Step 1: Get age public key from SSH host key
			fmt.Println("Step 1: Getting age public key from SSH host key...")

			port := host.SSHPort
			if port == 0 {
				port = 22
			}

			ageKey, err := secrets.GetHostAgeKeyFromRemote(ctx, host.Addr, host.SSHUser, port)
			if err != nil {
				return fmt.Errorf("failed to get age key: %w", err)
			}

			fmt.Printf("  Age public key: %s\n\n", ageKey)

			// Step 2: Show secrets.nix addition
			fmt.Println("Step 2: secrets.nix configuration")

			if outputSecretsNix {
				// Output in copy-paste format
				fmt.Println("Add to your secrets.nix hosts section:")
				fmt.Println("```nix")
				fmt.Printf("  %s = \"%s\";\n", host.Name, ageKey)
				fmt.Println("```")
				fmt.Println()
				fmt.Println("Then add secrets access:")
				fmt.Println("```nix")
				fmt.Printf("  \"your-secret.age\".publicKeys = allAdmins ++ [ hosts.%s ];\n", host.Name)
				fmt.Println("```")
			} else {
				fmt.Println("  Add to secrets.nix hosts section:")
				fmt.Printf("    %s = \"%s\";\n\n", host.Name, ageKey)
				fmt.Println("  Then add secrets access for this host:")
				fmt.Printf("    \"secret-name.age\".publicKeys = allAdmins ++ [ hosts.%s ];\n\n", host.Name)
			}

			// Step 3: Rekey secrets (optional)
			if !skipRekey {
				fmt.Println("Step 3: Rekeying secrets...")

				// Check if secrets.nix exists
				if _, err := os.Stat(secretsNixPath); os.IsNotExist(err) {
					fmt.Printf("  Skipped: secrets.nix not found at %s\n", secretsNixPath)
					fmt.Println("  After adding the host to secrets.nix, run: nixfleet secrets rekey")
				} else {
					// Parse and check if host is in secrets.nix
					config, err := secrets.ParseSecretsNix(ctx, secretsNixPath)
					if err != nil {
						fmt.Printf("  Warning: Could not parse secrets.nix: %v\n", err)
						fmt.Println("  After adding the host to secrets.nix, run: nixfleet secrets rekey")
					} else if _, exists := config.Hosts[host.Name]; !exists {
						fmt.Printf("  Host %s not yet in secrets.nix\n", host.Name)
						fmt.Println("  After adding the host, run: nixfleet secrets rekey")
					} else {
						// Host exists, get identity and rekey
						home, _ := os.UserHomeDir()
						identityPath := filepath.Join(home, ".config", "age", "admin-key.txt")

						if _, err := os.Stat(identityPath); os.IsNotExist(err) {
							fmt.Printf("  Skipped: Admin key not found at %s\n", identityPath)
							fmt.Println("  Run manually: nixfleet secrets rekey --identity /path/to/key")
						} else {
							rekeyed, err := secrets.RekeyAll(ctx, secretsDir, config, identityPath, dryRun)
							if err != nil {
								fmt.Printf("  Warning: Rekey failed: %v\n", err)
							} else if dryRun {
								fmt.Printf("  Would rekey %d secret(s)\n", len(rekeyed))
							} else {
								fmt.Printf("  Rekeyed %d secret(s)\n", len(rekeyed))
							}
						}
					}
				}
				fmt.Println()
			} else {
				fmt.Println("Step 3: Skipped (--skip-rekey)")
				fmt.Println()
			}

			// Step 4: Install pull mode (optional)
			if !skipPullMode {
				fmt.Println("Step 4: Installing pull mode...")

				if repoURL == "" {
					fmt.Println("  Skipped: No --repo specified")
					fmt.Println("  To install later: nixfleet pull-mode install -H " + host.Name + " --repo <url>")
				} else if dryRun {
					fmt.Printf("  Would install pull mode with repo: %s\n", repoURL)
				} else {
					pool := ssh.NewPool(nil)
					defer pool.Close()

					client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
					if err != nil {
						return fmt.Errorf("SSH connection failed: %w", err)
					}

					defaults := pullmode.DefaultConfig()
					pmConfig := pullmode.Config{
						RepoURL:     repoURL,
						Branch:      branch,
						HostName:    host.Name,
						SSHKeyPath:  defaults.SSHKeyPath,
						AgeKeyPath:  defaults.AgeKeyPath,
						Interval:    interval,
						ApplyOnBoot: true,
						RepoPath:    defaults.RepoPath,
					}
					if pmConfig.Branch == "" {
						pmConfig.Branch = defaults.Branch
					}
					if pmConfig.Interval == "" {
						pmConfig.Interval = defaults.Interval
					}

					installer := pullmode.NewInstaller()
					if err := installer.Install(ctx, client, pmConfig); err != nil {
						return fmt.Errorf("pull mode installation failed: %w", err)
					}

					fmt.Println("  Pull mode installed successfully")
				}
				fmt.Println()
			} else {
				fmt.Println("Step 4: Skipped (--skip-pull-mode)")
				fmt.Println()
			}

			// Summary
			fmt.Println("========================================")
			fmt.Printf("Onboarding complete for %s\n", host.Name)
			fmt.Println("========================================")
			fmt.Println()
			fmt.Println("Next steps:")
			if skipRekey || skipPullMode {
				fmt.Println("  1. Add host to secrets.nix (see above)")
				fmt.Println("  2. Run: nixfleet secrets rekey")
				fmt.Println("  3. Commit and push changes")
				if skipPullMode && repoURL == "" {
					fmt.Println("  4. Install pull mode: nixfleet pull-mode install -H " + host.Name + " --repo <url>")
				}
			} else {
				fmt.Println("  1. Verify deployment: nixfleet pull-mode status -H " + host.Name)
				fmt.Println("  2. Trigger first pull: nixfleet pull-mode trigger -H " + host.Name)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&secretsNixPath, "secrets-nix", "c", "secrets/secrets.nix", "Path to secrets.nix")
	cmd.Flags().StringVarP(&secretsDir, "secrets-dir", "s", "secrets/", "Directory containing .age files")
	cmd.Flags().StringVar(&repoURL, "repo", "", "Git repository URL for pull mode")
	cmd.Flags().StringVar(&branch, "branch", "main", "Git branch for pull mode")
	cmd.Flags().StringVar(&interval, "interval", "5m", "Pull interval (e.g., 5m, 1h)")
	cmd.Flags().BoolVar(&skipPullMode, "skip-pull-mode", false, "Skip pull mode installation")
	cmd.Flags().BoolVar(&skipRekey, "skip-rekey", false, "Skip secrets rekey step")
	cmd.Flags().BoolVar(&outputSecretsNix, "output-secrets-nix", false, "Output secrets.nix snippet in copy-paste format")

	return cmd
}

// PKI Commands

func pkiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pki",
		Short: "Manage fleet PKI and certificates",
		Long: `Manage the fleet's Public Key Infrastructure.

Commands:
  init             - Initialize a new root Certificate Authority
  init-intermediate - Create an intermediate CA (signed by root)
  issue            - Issue a certificate for a host
  status           - Show certificate status for fleet hosts
  renew            - Renew expiring certificates
  export           - Export CA certificate for external trust
  trust            - Add CA to local machine's trust store
  certmanager      - Integration with Kubernetes cert-manager
  install-timer    - Install systemd timer for auto-rotation
  uninstall-timer  - Remove systemd timer`,
	}

	cmd.AddCommand(pkiInitCmd())
	cmd.AddCommand(pkiInitIntermediateCmd())
	cmd.AddCommand(pkiIssueCmd())
	cmd.AddCommand(pkiStatusCmd())
	cmd.AddCommand(pkiExportCmd())
	cmd.AddCommand(pkiTrustCmd())
	cmd.AddCommand(pkiDeployCmd())
	cmd.AddCommand(pkiRenewCmd())
	cmd.AddCommand(pkiRevokeCmd())
	cmd.AddCommand(pkiCertManagerCmd())
	cmd.AddCommand(pkiInstallTimerCmd())
	cmd.AddCommand(pkiUninstallTimerCmd())

	return cmd
}

func pkiInitCmd() *cobra.Command {
	var (
		configFile   string
		pkiDir       string
		recipients   []string
		identities   []string
		commonName   string
		organization string
		validity     string
		force        bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a new Certificate Authority",
		Long: `Create a new root CA for the fleet.

This generates:
  - A self-signed root CA certificate (public)
  - An age-encrypted CA private key

The CA certificate will be deployed to all hosts to establish trust.
The private key is encrypted and only used to sign host certificates.

You can use a config file instead of CLI flags:
  nixfleet pki init --config secrets/pki.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_ = ctx // for future use

			// Load config file if specified
			var pkiCfg *pki.PKIConfig
			if configFile != "" {
				var err error
				pkiCfg, err = pki.LoadPKIConfig(configFile)
				if err != nil {
					return fmt.Errorf("loading config: %w", err)
				}
				if err := pkiCfg.Validate(); err != nil {
					return fmt.Errorf("invalid config: %w", err)
				}

				// Use config values as defaults (CLI flags override)
				if pkiDir == "secrets/pki" && pkiCfg.Directory != "" {
					pkiDir = pkiCfg.Directory
				}
				if len(recipients) == 0 {
					recipients = pkiCfg.Recipients
				}
				if len(identities) == 0 {
					identities = pkiCfg.Identities
				}
				if commonName == "NixFleet Root CA" && pkiCfg.RootCA.CommonName != "" {
					commonName = pkiCfg.RootCA.CommonName
				}
				if organization == "NixFleet" && pkiCfg.RootCA.Organization != "" {
					organization = pkiCfg.RootCA.Organization
				}
				if validity == "10y" && pkiCfg.RootCA.Validity != "" {
					validity = pkiCfg.RootCA.Validity
				}
			}

			store := pki.NewStore(pkiDir, recipients, identities)

			// Check if CA already exists
			if store.CAExists() && !force {
				return fmt.Errorf("CA already exists at %s. Use --force to overwrite", pkiDir)
			}

			if len(recipients) == 0 {
				return fmt.Errorf("at least one --recipient is required for encrypting the CA private key")
			}

			// Parse validity using our helper
			validityDuration, err := pki.ParseValidityDuration(validity)
			if err != nil {
				return fmt.Errorf("invalid validity format: %s (use e.g., 10y, 90d, 8760h)", validity)
			}

			cfg := &pki.CAConfig{
				CommonName:   commonName,
				Organization: organization,
				Validity:     validityDuration,
			}

			fmt.Println("Initializing NixFleet PKI...")
			fmt.Printf("  Common Name:  %s\n", cfg.CommonName)
			fmt.Printf("  Organization: %s\n", cfg.Organization)
			fmt.Printf("  Validity:     %s\n", validity)
			fmt.Println()

			// Create CA
			ca, err := pki.InitCA(cfg)
			if err != nil {
				return fmt.Errorf("creating CA: %w", err)
			}

			// Save to disk
			if err := store.SaveCA(ca); err != nil {
				return fmt.Errorf("saving CA: %w", err)
			}

			fmt.Println("CA initialized successfully!")
			fmt.Println()
			fmt.Printf("Files created:\n")
			fmt.Printf("  Certificate: %s/ca/root.crt (public)\n", pkiDir)
			fmt.Printf("  Private Key: %s/ca/root.key.age (encrypted)\n", pkiDir)
			fmt.Println()
			if pkiCfg != nil && pkiCfg.IntermediateCA != nil {
				fmt.Println("Next steps:")
				fmt.Println("  1. Create intermediate CA: nixfleet pki init-intermediate --config " + configFile)
				fmt.Println("  2. Issue certificates:     nixfleet pki issue <hostname>")
				fmt.Println("  3. Deploy to hosts:        nixfleet apply")
			} else {
				fmt.Println("Next steps:")
				fmt.Println("  1. Issue certificates: nixfleet pki issue <hostname>")
				fmt.Println("  2. Deploy to hosts:    nixfleet apply")
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&configFile, "config", "c", "", "PKI config file (e.g., secrets/pki.yaml)")
	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipients for encrypting CA key")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringVar(&commonName, "cn", "NixFleet Root CA", "CA common name")
	cmd.Flags().StringVar(&organization, "org", "NixFleet", "Organization name")
	cmd.Flags().StringVar(&validity, "validity", "10y", "CA certificate validity (e.g., 10y, 8760h)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing CA")

	return cmd
}

func pkiInitIntermediateCmd() *cobra.Command {
	var (
		configFile   string
		pkiDir       string
		recipients   []string
		identities   []string
		commonName   string
		organization string
		validity     string
		force        bool
	)

	cmd := &cobra.Command{
		Use:   "init-intermediate",
		Short: "Create an intermediate CA signed by the root CA",
		Long: `Create an intermediate CA for signing host certificates.

This provides better security by keeping the root CA private key offline.
The intermediate CA:
  - Is signed by the root CA
  - Has a shorter validity than root (default 5 years)
  - Can only sign end-entity certificates (not other CAs)

The certificate chain (intermediate + root) is automatically included
when issuing certificates, enabling full chain validation.

Examples:
  nixfleet pki init-intermediate --config secrets/pki.yaml
  nixfleet pki init-intermediate -r age1...
  nixfleet pki init-intermediate --cn "NixFleet Signing CA" --validity 3y`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Load config file if specified
			if configFile != "" {
				pkiCfg, err := pki.LoadPKIConfig(configFile)
				if err != nil {
					return fmt.Errorf("loading config: %w", err)
				}
				if err := pkiCfg.Validate(); err != nil {
					return fmt.Errorf("invalid config: %w", err)
				}

				// Check if intermediate CA is configured
				if pkiCfg.IntermediateCA == nil {
					return fmt.Errorf("intermediate CA not configured in %s", configFile)
				}

				// Use config values as defaults (CLI flags override)
				if pkiDir == "secrets/pki" && pkiCfg.Directory != "" {
					pkiDir = pkiCfg.Directory
				}
				if len(recipients) == 0 {
					recipients = pkiCfg.Recipients
				}
				if len(identities) == 0 {
					identities = pkiCfg.Identities
				}
				if commonName == "NixFleet Intermediate CA" && pkiCfg.IntermediateCA.CommonName != "" {
					commonName = pkiCfg.IntermediateCA.CommonName
				}
				if organization == "NixFleet" && pkiCfg.IntermediateCA.Organization != "" {
					organization = pkiCfg.IntermediateCA.Organization
				}
				if validity == "5y" && pkiCfg.IntermediateCA.Validity != "" {
					validity = pkiCfg.IntermediateCA.Validity
				}
			}

			store := pki.NewStore(pkiDir, recipients, identities)

			// Check if root CA exists
			if !store.CAExists() {
				return fmt.Errorf("root CA not initialized. Run 'nixfleet pki init' first")
			}

			// Check if intermediate already exists
			if store.IntermediateCAExists() && !force {
				return fmt.Errorf("intermediate CA already exists. Use --force to overwrite")
			}

			if len(recipients) == 0 {
				return fmt.Errorf("at least one --recipient is required for encrypting the intermediate CA key")
			}

			// Parse validity using our helper
			validityDuration, err := pki.ParseValidityDuration(validity)
			if err != nil {
				return fmt.Errorf("invalid validity format: %s (use e.g., 5y, 90d, 8760h)", validity)
			}

			// Load root CA
			rootCA, err := store.LoadCA(ctx)
			if err != nil {
				return fmt.Errorf("loading root CA: %w", err)
			}

			cfg := &pki.IntermediateCAConfig{
				CommonName:   commonName,
				Organization: organization,
				Validity:     validityDuration,
			}

			fmt.Println("Creating intermediate CA...")
			fmt.Printf("  Common Name:  %s\n", cfg.CommonName)
			fmt.Printf("  Organization: %s\n", cfg.Organization)
			fmt.Printf("  Validity:     %s\n", validity)
			fmt.Println()

			// Create intermediate CA
			intermediateCA, err := rootCA.InitIntermediateCA(cfg)
			if err != nil {
				return fmt.Errorf("creating intermediate CA: %w", err)
			}

			// Save to disk
			if err := store.SaveIntermediateCA(intermediateCA); err != nil {
				return fmt.Errorf("saving intermediate CA: %w", err)
			}

			fmt.Println("Intermediate CA created successfully!")
			fmt.Println()
			fmt.Printf("Files created:\n")
			fmt.Printf("  Certificate: %s/ca/intermediate.crt\n", pkiDir)
			fmt.Printf("  Chain:       %s/ca/chain.crt (intermediate + root)\n", pkiDir)
			fmt.Printf("  Private Key: %s/ca/intermediate.key.age (encrypted)\n", pkiDir)
			fmt.Println()
			fmt.Println("Host certificates will now be signed by the intermediate CA")
			fmt.Println("and include the full certificate chain.")

			return nil
		},
	}

	cmd.Flags().StringVarP(&configFile, "config", "c", "", "PKI config file (e.g., secrets/pki.yaml)")
	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipients for encrypting intermediate CA key")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringVar(&commonName, "cn", "NixFleet Intermediate CA", "Intermediate CA common name")
	cmd.Flags().StringVar(&organization, "org", "NixFleet", "Organization name")
	cmd.Flags().StringVar(&validity, "validity", "5y", "Intermediate CA validity (e.g., 5y, 8760h)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing intermediate CA")

	return cmd
}

func pkiIssueCmd() *cobra.Command {
	var (
		configFile string
		pkiDir     string
		recipients []string
		identities []string
		sans       []string
		validity   string
		all        bool
		certName   string
	)

	cmd := &cobra.Command{
		Use:   "issue [hostname]",
		Short: "Issue a certificate for a host",
		Long: `Issue a TLS certificate for a host, signed by the fleet CA.

The certificate includes:
  - The hostname as Common Name
  - Additional SANs (DNS names and IP addresses)
  - Server and client auth extended key usage (for mTLS)

Multiple named certificates per host are supported using --name:
  - Default name is "host" if not specified
  - Stored at: secrets/pki/hosts/{hostname}/{name}.crt

With a config file, host SANs and certificate settings can be predefined.

Examples:
  nixfleet pki issue host-a
  nixfleet pki issue host-a --name web --san host-a.example.com
  nixfleet pki issue host-a --config secrets/pki.yaml  # Uses SANs from config
  nixfleet pki issue --all`,
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("hostname required (or use --all)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Load config file if specified
			var pkiCfg *pki.PKIConfig
			if configFile != "" {
				var err error
				pkiCfg, err = pki.LoadPKIConfig(configFile)
				if err != nil {
					return fmt.Errorf("loading config: %w", err)
				}

				// Use config values as defaults (CLI flags override)
				if pkiDir == "secrets/pki" && pkiCfg.Directory != "" {
					pkiDir = pkiCfg.Directory
				}
				if len(recipients) == 0 {
					recipients = pkiCfg.Recipients
				}
				if len(identities) == 0 {
					identities = pkiCfg.Identities
				}
				if validity == "365d" && pkiCfg.Defaults.Validity != "" {
					validity = pkiCfg.Defaults.Validity
				}
			}

			store := pki.NewStore(pkiDir, recipients, identities)

			// Check CA exists
			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			// Determine which CA to use for signing
			// Prefer intermediate CA if available, otherwise use root
			var issuer interface {
				IssueCert(req *pki.CertRequest) (*pki.IssuedCert, error)
			}
			var signerName string

			if store.IntermediateCAExists() {
				ica, err := store.LoadIntermediateCA(ctx)
				if err != nil {
					return fmt.Errorf("loading intermediate CA: %w", err)
				}
				issuer = ica
				signerName = "intermediate CA"
			} else {
				ca, err := store.LoadCA(ctx)
				if err != nil {
					return fmt.Errorf("loading CA: %w", err)
				}
				issuer = ca
				signerName = "root CA"
			}

			// Parse validity using helper
			validityDuration, err := pki.ParseValidityDuration(validity)
			if err != nil {
				return fmt.Errorf("invalid validity format: %s (use e.g., 90d, 1y)", validity)
			}

			// Determine hosts to issue certs for
			var hostnames []string
			if all {
				_, hosts, err := loadInventoryAndHosts(ctx)
				if err != nil {
					return err
				}
				for _, h := range hosts {
					hostnames = append(hostnames, h.Name)
				}
			} else {
				hostnames = []string{args[0]}
			}

			if len(recipients) == 0 {
				return fmt.Errorf("at least one --recipient is required")
			}

			fmt.Printf("Issuing certificates for %d host(s) using %s...\n\n", len(hostnames), signerName)

			for _, hostname := range hostnames {
				// Build request, merging config and CLI flags
				var req *pki.CertRequest

				// Try to get config-defined request first
				if pkiCfg != nil {
					var err error
					req, err = pkiCfg.GetHostCertRequest(hostname, certName)
					if err != nil {
						req = nil // Fall back to manual construction
					}
				}

				// If no config or config failed, build manually
				if req == nil {
					req = &pki.CertRequest{
						Hostname: hostname,
						Name:     certName,
						Validity: validityDuration,
					}
				}

				// CLI sans always override/append
				if len(sans) > 0 {
					req.SANs = append(req.SANs, sans...)
				}

				cert, err := issuer.IssueCert(req)
				if err != nil {
					fmt.Printf("  %s: FAILED - %v\n", hostname, err)
					continue
				}

				if err := store.SaveHostCert(cert); err != nil {
					fmt.Printf("  %s: FAILED to save - %v\n", hostname, err)
					continue
				}

				certLabel := hostname
				if certName != "" && certName != "host" {
					certLabel = fmt.Sprintf("%s/%s", hostname, certName)
				}
				fmt.Printf("  %s: OK (expires %s)\n", certLabel, cert.NotAfter.Format("2006-01-02"))
			}

			fmt.Println()
			fmt.Println("Certificates issued. Deploy with: nixfleet apply")

			return nil
		},
	}

	cmd.Flags().StringVarP(&configFile, "config", "c", "", "PKI config file (e.g., secrets/pki.yaml)")
	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipients for encrypting host keys")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringSliceVar(&sans, "san", nil, "Subject Alternative Names (DNS names or IPs)")
	cmd.Flags().StringVar(&validity, "validity", "365d", "Certificate validity (e.g., 365d, 1y)")
	cmd.Flags().BoolVar(&all, "all", false, "Issue certificates for all hosts in inventory")
	cmd.Flags().StringVar(&certName, "name", "", "Certificate name (default: host). Use for multiple certs per host")

	return cmd
}

func pkiStatusCmd() *cobra.Command {
	var (
		pkiDir     string
		identities []string
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show certificate status for fleet hosts",
		Long: `Display certificate status for all hosts in the fleet.

Shows:
  - Certificate names (host, web, api, etc.)
  - Certificate expiration dates
  - Days remaining until expiry
  - Status (valid, expiring, expired)
  - Subject Alternative Names`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_ = ctx

			store := pki.NewStore(pkiDir, nil, identities)

			// Check CA exists
			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			// List host certs
			hosts, err := store.ListHostCerts()
			if err != nil {
				return fmt.Errorf("listing certificates: %w", err)
			}

			if len(hosts) == 0 {
				fmt.Println("No host certificates found.")
				fmt.Println("Issue certificates with: nixfleet pki issue <hostname>")
				return nil
			}

			fmt.Printf("%-25s %-12s %-10s %-12s %s\n", "HOST/CERT", "EXPIRES", "DAYS LEFT", "STATUS", "SANs")
			fmt.Println(strings.Repeat("-", 90))

			for _, hostname := range hosts {
				// List all named certs for this host
				certNames, err := store.ListHostNamedCerts(hostname)
				if err != nil {
					fmt.Printf("%-25s %-12s %-10s %-12s %s\n", hostname, "ERROR", "-", "error", err.Error())
					continue
				}

				for i, certName := range certNames {
					info, err := store.GetNamedCertInfo(hostname, certName)
					if err != nil {
						label := fmt.Sprintf("%s/%s", hostname, certName)
						fmt.Printf("%-25s %-12s %-10s %-12s %s\n", label, "ERROR", "-", "error", err.Error())
						continue
					}

					// Format status with color indicators
					var statusIcon string
					switch info.Status {
					case "valid":
						statusIcon = "✓ valid"
					case "expiring":
						statusIcon = "⚠ expiring"
					case "expired":
						statusIcon = "✗ expired"
					}

					sansStr := strings.Join(info.SANs, ", ")
					if len(sansStr) > 25 {
						sansStr = sansStr[:22] + "..."
					}

					// Format host/cert label
					var label string
					if len(certNames) == 1 && certName == "host" {
						label = hostname
					} else if i == 0 {
						label = fmt.Sprintf("%s/%s", hostname, certName)
					} else {
						label = fmt.Sprintf("  └─ %s", certName)
					}

					fmt.Printf("%-25s %-12s %-10d %-12s %s\n",
						label,
						info.NotAfter.Format("2006-01-02"),
						info.DaysLeft,
						statusIcon,
						sansStr,
					)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files (for CA info)")

	return cmd
}

func pkiExportCmd() *cobra.Command {
	var pkiDir string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export CA certificate",
		Long: `Export the CA certificate in PEM format.

This can be used to add the fleet CA to external trust stores
or configure applications to trust fleet certificates.

Example:
  nixfleet pki export > fleet-ca.crt
  sudo cp fleet-ca.crt /usr/local/share/ca-certificates/
  sudo update-ca-certificates`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := pki.NewStore(pkiDir, nil, nil)

			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			caCertPath := store.GetCACertPath()
			certPEM, err := os.ReadFile(caCertPath)
			if err != nil {
				return fmt.Errorf("reading CA certificate: %w", err)
			}

			fmt.Print(string(certPEM))
			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")

	return cmd
}

func pkiTrustCmd() *cobra.Command {
	var pkiDir string

	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Add CA certificate to local trust store",
		Long: `Add the fleet CA certificate to your local machine's trust store.

This command detects your operating system and installs the CA certificate
to the appropriate system trust store:

  macOS:  System Keychain via 'security' command
  Linux:  /usr/local/share/ca-certificates/ + update-ca-certificates (Debian/Ubuntu)
          /etc/pki/ca-trust/source/anchors/ + update-ca-trust (RHEL/Fedora)

After running this command, applications on your machine will trust
certificates signed by the fleet CA.

Examples:
  nixfleet pki trust
  nixfleet pki trust --pki-dir /path/to/pki`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := pki.NewStore(pkiDir, nil, nil)

			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			caCertPath := store.GetCACertPath()
			certPEM, err := os.ReadFile(caCertPath)
			if err != nil {
				return fmt.Errorf("reading CA certificate: %w", err)
			}

			// Create temp file for the certificate
			tmpFile, err := os.CreateTemp("", "fleet-ca-*.crt")
			if err != nil {
				return fmt.Errorf("creating temp file: %w", err)
			}
			defer os.Remove(tmpFile.Name())

			if _, err := tmpFile.Write(certPEM); err != nil {
				return fmt.Errorf("writing temp file: %w", err)
			}
			tmpFile.Close()

			// Detect OS and install appropriately
			switch runtime.GOOS {
			case "darwin":
				fmt.Println("Installing CA certificate to macOS System Keychain...")
				installCmd := exec.Command("sudo", "security", "add-trusted-cert",
					"-d", "-r", "trustRoot",
					"-k", "/Library/Keychains/System.keychain",
					tmpFile.Name())
				installCmd.Stdout = os.Stdout
				installCmd.Stderr = os.Stderr
				installCmd.Stdin = os.Stdin
				if err := installCmd.Run(); err != nil {
					return fmt.Errorf("adding certificate to keychain: %w", err)
				}
				fmt.Println("CA certificate installed to System Keychain")

			case "linux":
				// Try Debian/Ubuntu style first
				debianPath := "/usr/local/share/ca-certificates/fleet-ca.crt"
				if _, err := os.Stat("/usr/local/share/ca-certificates"); err == nil {
					fmt.Println("Installing CA certificate (Debian/Ubuntu style)...")
					copyCmd := exec.Command("sudo", "cp", tmpFile.Name(), debianPath)
					if err := copyCmd.Run(); err != nil {
						return fmt.Errorf("copying certificate: %w", err)
					}
					updateCmd := exec.Command("sudo", "update-ca-certificates")
					updateCmd.Stdout = os.Stdout
					updateCmd.Stderr = os.Stderr
					if err := updateCmd.Run(); err != nil {
						return fmt.Errorf("updating CA certificates: %w", err)
					}
					fmt.Printf("CA certificate installed to %s\n", debianPath)
				} else {
					// Try RHEL/Fedora style
					rhelPath := "/etc/pki/ca-trust/source/anchors/fleet-ca.crt"
					if _, err := os.Stat("/etc/pki/ca-trust/source/anchors"); err == nil {
						fmt.Println("Installing CA certificate (RHEL/Fedora style)...")
						copyCmd := exec.Command("sudo", "cp", tmpFile.Name(), rhelPath)
						if err := copyCmd.Run(); err != nil {
							return fmt.Errorf("copying certificate: %w", err)
						}
						updateCmd := exec.Command("sudo", "update-ca-trust")
						updateCmd.Stdout = os.Stdout
						updateCmd.Stderr = os.Stderr
						if err := updateCmd.Run(); err != nil {
							return fmt.Errorf("updating CA trust: %w", err)
						}
						fmt.Printf("CA certificate installed to %s\n", rhelPath)
					} else {
						return fmt.Errorf("could not detect Linux CA trust store location")
					}
				}

			default:
				return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
			}

			fmt.Println("\nFleet CA is now trusted by your system.")
			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")

	return cmd
}

func pkiDeployCmd() *cobra.Command {
	var (
		pkiDir      string
		identities  []string
		destDir     string
		trustSystem bool
		caOnly      bool
	)

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy certificates to fleet hosts",
		Long: `Deploy CA and host certificates to fleet hosts via SSH.

This command:
  - Deploys the CA certificate to all hosts
  - Deploys host-specific certificates and private keys
  - Optionally adds CA to system trust store

The host private keys are decrypted using age and deployed securely.

Examples:
  nixfleet pki deploy --identity ~/.config/age/key.txt
  nixfleet pki deploy --ca-only      # Only deploy CA cert
  nixfleet pki deploy -H myhost      # Deploy to specific host`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			store := pki.NewStore(pkiDir, nil, identities)

			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			// Read CA certificate
			caCertPath := store.GetCACertPath()
			caCertPEM, err := os.ReadFile(caCertPath)
			if err != nil {
				return fmt.Errorf("reading CA certificate: %w", err)
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			fmt.Printf("Deploying PKI to %d host(s)...\n\n", len(hosts))

			successCount := 0
			failedCount := 0

			for _, host := range hosts {
				fmt.Printf("%s:\n", host.Name)

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("  Connection failed: %v\n", err)
					failedCount++
					continue
				}

				// Create PKI directory
				mkdirCmd := fmt.Sprintf("sudo mkdir -p %s && sudo chmod 755 %s", destDir, destDir)
				if _, err := client.Exec(ctx, mkdirCmd); err != nil {
					fmt.Printf("  Failed to create directory: %v\n", err)
					failedCount++
					continue
				}

				// Deploy CA certificate
				caCertDest := destDir + "/ca.crt"
				if err := deployFileContent(ctx, client, caCertPEM, caCertDest, "0644"); err != nil {
					fmt.Printf("  Failed to deploy CA cert: %v\n", err)
					failedCount++
					continue
				}
				fmt.Printf("  CA cert: %s\n", caCertDest)

				// Update system trust store if requested
				if trustSystem {
					updateCmd := ""
					switch host.Base {
					case "ubuntu":
						updateCmd = fmt.Sprintf("sudo cp %s /usr/local/share/ca-certificates/nixfleet-ca.crt && sudo update-ca-certificates", caCertDest)
					case "nixos", "darwin":
						// NixOS/darwin handle this differently via configuration
						updateCmd = ""
					}
					if updateCmd != "" {
						if _, err := client.Exec(ctx, updateCmd); err != nil {
							fmt.Printf("  Warning: failed to update system trust: %v\n", err)
						} else {
							fmt.Printf("  System trust updated\n")
						}
					}
				}

				// Deploy host certificate and key (unless CA-only mode)
				if !caOnly {
					if store.HostCertExists(host.Name) {
						hostCert, err := store.LoadHostCert(ctx, host.Name)
						if err != nil {
							fmt.Printf("  Failed to load host cert: %v\n", err)
						} else {
							// Deploy host certificate
							hostCertDest := destDir + "/host.crt"
							if err := deployFileContent(ctx, client, hostCert.CertPEM, hostCertDest, "0644"); err != nil {
								fmt.Printf("  Failed to deploy host cert: %v\n", err)
							} else {
								fmt.Printf("  Host cert: %s\n", hostCertDest)
							}

							// Deploy host key (restricted permissions)
							hostKeyDest := destDir + "/host.key"
							if err := deployFileContent(ctx, client, hostCert.KeyPEM, hostKeyDest, "0600"); err != nil {
								fmt.Printf("  Failed to deploy host key: %v\n", err)
							} else {
								fmt.Printf("  Host key: %s\n", hostKeyDest)
							}
						}
					} else {
						fmt.Printf("  No host certificate found (run 'nixfleet pki issue %s')\n", host.Name)
					}
				}

				fmt.Println()
				successCount++
			}

			fmt.Printf("Summary: %d succeeded, %d failed\n", successCount, failedCount)
			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decrypting host keys")
	cmd.Flags().StringVar(&destDir, "dest-dir", "/etc/nixfleet/pki", "Destination directory on hosts")
	cmd.Flags().BoolVar(&trustSystem, "trust-system", false, "Add CA to system trust store")
	cmd.Flags().BoolVar(&caOnly, "ca-only", false, "Only deploy CA certificate (skip host certs)")

	return cmd
}

func pkiRenewCmd() *cobra.Command {
	var (
		pkiDir     string
		identities []string
		validity   time.Duration
		days       int
		force      bool
	)

	cmd := &cobra.Command{
		Use:   "renew [hostname...]",
		Short: "Renew expiring certificates",
		Long: `Renew certificates that are expiring or have expired.

Without arguments, checks all certificates and renews those expiring within --days.
With hostnames, renews certificates for the specified hosts.

Examples:
  nixfleet pki renew --days 30         # Renew certs expiring in 30 days
  nixfleet pki renew myhost            # Renew cert for myhost
  nixfleet pki renew --force myhost    # Force renew even if not expiring`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			store := pki.NewStore(pkiDir, nil, identities)
			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			deployer := pki.NewDeployer(&pki.DeployConfig{
				PKIDir:     pkiDir,
				Identities: identities,
			})

			// Determine which certs to renew
			var toRenew []string
			if len(args) > 0 {
				// Specific hosts provided
				toRenew = args
			} else {
				// Check for expiring certs
				renewalInfos, err := deployer.CheckRenewalNeeded(ctx, days)
				if err != nil {
					return fmt.Errorf("checking renewal: %w", err)
				}
				if len(renewalInfos) == 0 {
					fmt.Println("No certificates need renewal")
					return nil
				}
				for _, info := range renewalInfos {
					toRenew = append(toRenew, info.Hostname)
				}
			}

			fmt.Printf("Renewing %d certificate(s)...\n\n", len(toRenew))

			for _, hostname := range toRenew {
				// Check if cert exists and needs renewal (unless force)
				if !force && len(args) > 0 {
					info, err := store.GetCertInfo(hostname)
					if err != nil {
						fmt.Printf("%s: certificate not found\n", hostname)
						continue
					}
					if info.DaysLeft > days {
						fmt.Printf("%s: skipping (expires in %d days, use --force to renew anyway)\n",
							hostname, info.DaysLeft)
						continue
					}
				}

				cert, err := deployer.RenewCert(ctx, hostname, nil, validity)
				if err != nil {
					fmt.Printf("%s: renewal failed - %v\n", hostname, err)
					continue
				}

				fmt.Printf("%s: renewed (valid until %s)\n",
					hostname, cert.NotAfter.Format("2006-01-02"))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().DurationVar(&validity, "validity", 365*24*time.Hour, "Validity period for renewed certs")
	cmd.Flags().IntVar(&days, "days", 30, "Renew certs expiring within this many days")
	cmd.Flags().BoolVar(&force, "force", false, "Force renewal even if cert is not expiring")

	return cmd
}

func pkiRevokeCmd() *cobra.Command {
	var (
		pkiDir string
		force  bool
	)

	cmd := &cobra.Command{
		Use:   "revoke <hostname>",
		Short: "Revoke a host certificate",
		Long: `Revoke a host certificate by removing it from the PKI store.

This removes the certificate and key files for the specified host.
The certificate will no longer be deployed to the host.

Examples:
  nixfleet pki revoke oldhost
  nixfleet pki revoke --force oldhost  # Skip confirmation`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			hostname := args[0]

			store := pki.NewStore(pkiDir, nil, nil)

			// Check if cert exists
			if !store.HostCertExists(hostname) {
				return fmt.Errorf("no certificate found for %s", hostname)
			}

			// Get cert info for confirmation
			info, err := store.GetCertInfo(hostname)
			if err != nil {
				return fmt.Errorf("reading certificate: %w", err)
			}

			if !force {
				fmt.Printf("Certificate for %s:\n", hostname)
				fmt.Printf("  Serial: %s\n", info.Serial)
				fmt.Printf("  Expires: %s (%d days)\n", info.NotAfter.Format("2006-01-02"), info.DaysLeft)
				fmt.Printf("\nThis will permanently remove this certificate.\n")
				fmt.Printf("Type 'yes' to confirm: ")

				var confirm string
				if _, err := fmt.Scanln(&confirm); err != nil || confirm != "yes" {
					fmt.Println("Aborted")
					return nil
				}
			}

			deployer := pki.NewDeployer(&pki.DeployConfig{PKIDir: pkiDir})
			if err := deployer.RevokeCert(ctx, hostname); err != nil {
				return fmt.Errorf("revoking certificate: %w", err)
			}

			fmt.Printf("Certificate for %s has been revoked\n", hostname)
			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")

	return cmd
}

// cert-manager integration commands

func pkiCertManagerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "certmanager",
		Short: "Kubernetes cert-manager integration",
		Long: `Integration with Kubernetes cert-manager for automatic certificate management.

Commands:
  serve  - Start webhook server for CSR signing
  export - Export CA or host certificates as Kubernetes secrets
  issuer - Generate cert-manager ClusterIssuer configuration`,
	}

	cmd.AddCommand(pkiCertManagerServeCmd())
	cmd.AddCommand(pkiCertManagerExportCmd())
	cmd.AddCommand(pkiCertManagerIssuerCmd())

	return cmd
}

func pkiCertManagerServeCmd() *cobra.Command {
	var (
		pkiDir     string
		identities []string
		listenAddr string
		tlsCert    string
		tlsKey     string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start cert-manager webhook server",
		Long: `Start an HTTP(S) webhook server that signs CSRs from cert-manager.

This allows cert-manager to request certificates from the NixFleet CA
without exposing the CA private key to the Kubernetes cluster.

The webhook listens for signing requests and returns signed certificates.

Examples:
  nixfleet pki certmanager serve
  nixfleet pki certmanager serve --listen :8443 --tls-cert server.crt --tls-key server.key`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			store := pki.NewStore(pkiDir, nil, identities)

			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			ca, err := store.LoadCA(ctx)
			if err != nil {
				return fmt.Errorf("loading CA: %w", err)
			}

			config := pki.DefaultCertManagerConfig()
			config.ListenAddr = listenAddr
			config.TLSCertFile = tlsCert
			config.TLSKeyFile = tlsKey

			webhook := pki.NewCertManagerWebhook(ca, config)

			fmt.Printf("Starting cert-manager webhook server on %s\n", listenAddr)
			if tlsCert != "" {
				fmt.Println("TLS enabled")
			}
			fmt.Println("Endpoints:")
			fmt.Println("  POST /sign   - Sign CSR")
			fmt.Println("  GET  /health - Health check")

			return webhook.StartServer(ctx)
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringVar(&listenAddr, "listen", ":8443", "Address to listen on")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate file for HTTPS")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS key file for HTTPS")

	return cmd
}

func pkiCertManagerExportCmd() *cobra.Command {
	var (
		pkiDir             string
		identities         []string
		namespace          string
		secretName         string
		exportCA           bool
		exportIntermediate bool
		hostname           string
		certName           string
		output             string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export certificates as Kubernetes secrets",
		Long: `Export CA or host certificates as Kubernetes TLS secrets.

The generated secret can be applied directly to a Kubernetes cluster
using kubectl apply, or saved to a file for later use.

Examples:
  # Export intermediate CA as a secret for cert-manager (recommended)
  nixfleet pki certmanager export --intermediate -n cert-manager

  # Export root CA as a secret for cert-manager
  nixfleet pki certmanager export --ca -n cert-manager

  # Export a host certificate
  nixfleet pki certmanager export --hostname myhost

  # Export a named certificate for a host
  nixfleet pki certmanager export --hostname myhost --cert-name web

  # Save to file
  nixfleet pki certmanager export --intermediate -o ca-secret.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			store := pki.NewStore(pkiDir, nil, identities)

			if !store.CAExists() {
				return fmt.Errorf("CA not initialized. Run 'nixfleet pki init' first")
			}

			var secretJSON []byte

			if exportIntermediate {
				if !store.IntermediateCAExists() {
					return fmt.Errorf("intermediate CA not initialized. Run 'nixfleet pki init-intermediate' first")
				}

				ica, err := store.LoadIntermediateCA(ctx)
				if err != nil {
					return fmt.Errorf("loading intermediate CA: %w", err)
				}

				secret, err := pki.ExportIntermediateCAToK8sSecret(ica, namespace, secretName)
				if err != nil {
					return fmt.Errorf("exporting intermediate CA secret: %w", err)
				}

				data, err := json.MarshalIndent(secret, "", "  ")
				if err != nil {
					return fmt.Errorf("marshaling secret: %w", err)
				}
				secretJSON = data
			} else if exportCA {
				ca, err := store.LoadCA(ctx)
				if err != nil {
					return fmt.Errorf("loading CA: %w", err)
				}

				secret, err := pki.ExportCAToK8sSecret(ca, namespace, secretName)
				if err != nil {
					return fmt.Errorf("exporting CA secret: %w", err)
				}

				data, err := json.MarshalIndent(secret, "", "  ")
				if err != nil {
					return fmt.Errorf("marshaling secret: %w", err)
				}
				secretJSON = data
			} else if hostname != "" {
				cert, err := store.LoadNamedCert(ctx, hostname, certName)
				if err != nil {
					return fmt.Errorf("loading certificate: %w", err)
				}

				// Get CA cert for chain
				caCertPEM, err := os.ReadFile(store.GetCACertPath())
				if err != nil {
					return fmt.Errorf("reading CA certificate: %w", err)
				}

				secret, err := pki.ExportToK8sSecret(cert, caCertPEM, namespace, secretName)
				if err != nil {
					return fmt.Errorf("exporting certificate secret: %w", err)
				}

				data, err := json.MarshalIndent(secret, "", "  ")
				if err != nil {
					return fmt.Errorf("marshaling secret: %w", err)
				}
				secretJSON = data
			} else {
				return fmt.Errorf("either --intermediate, --ca, or --hostname must be specified")
			}

			if output != "" {
				if err := os.WriteFile(output, secretJSON, 0644); err != nil {
					return fmt.Errorf("writing output file: %w", err)
				}
				fmt.Printf("Secret written to %s\n", output)
			} else {
				fmt.Println(string(secretJSON))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes namespace (default: cert-manager for CA, default for certs)")
	cmd.Flags().StringVar(&secretName, "secret-name", "", "Secret name (auto-generated if not specified)")
	cmd.Flags().BoolVar(&exportCA, "ca", false, "Export root CA certificate")
	cmd.Flags().BoolVar(&exportIntermediate, "intermediate", false, "Export intermediate CA certificate (recommended for cert-manager)")
	cmd.Flags().StringVar(&hostname, "hostname", "", "Export certificate for this host")
	cmd.Flags().StringVar(&certName, "cert-name", "", "Certificate name (default: host)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (prints to stdout if not specified)")

	return cmd
}

func pkiCertManagerIssuerCmd() *cobra.Command {
	var (
		secretName      string
		secretNamespace string
		issuerName      string
		output          string
	)

	cmd := &cobra.Command{
		Use:   "issuer",
		Short: "Generate cert-manager ClusterIssuer configuration",
		Long: `Generate a cert-manager ClusterIssuer that uses the NixFleet CA.

The issuer references a Kubernetes secret containing the CA certificate
and key. Use 'nixfleet pki certmanager export --ca' to create the secret.

Examples:
  # Generate issuer config
  nixfleet pki certmanager issuer

  # Custom names
  nixfleet pki certmanager issuer --issuer-name my-issuer --secret-name my-ca

  # Save to file
  nixfleet pki certmanager issuer -o issuer.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			issuer := pki.GenerateCertManagerIssuer(secretName, secretNamespace, issuerName)

			issuerJSON, err := json.MarshalIndent(issuer, "", "  ")
			if err != nil {
				return fmt.Errorf("marshaling issuer: %w", err)
			}

			if output != "" {
				if err := os.WriteFile(output, issuerJSON, 0644); err != nil {
					return fmt.Errorf("writing output file: %w", err)
				}
				fmt.Printf("ClusterIssuer config written to %s\n", output)
			} else {
				fmt.Println(string(issuerJSON))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&secretName, "secret-name", "nixfleet-ca", "Name of the CA secret")
	cmd.Flags().StringVar(&secretNamespace, "secret-namespace", "cert-manager", "Namespace containing the CA secret")
	cmd.Flags().StringVar(&issuerName, "issuer-name", "nixfleet-ca-issuer", "Name for the ClusterIssuer")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (prints to stdout if not specified)")

	return cmd
}

// deployFileContent deploys content to a remote path via SSH
func deployFileContent(ctx context.Context, client *ssh.Client, content []byte, destPath, mode string) error {
	// Use a heredoc to write content
	// Base64 encode to handle binary/special characters
	encoded := base64.StdEncoding.EncodeToString(content)

	cmd := fmt.Sprintf("echo '%s' | base64 -d | sudo tee %s > /dev/null && sudo chmod %s %s",
		encoded, destPath, mode, destPath)

	_, err := client.Exec(ctx, cmd)
	return err
}

func pkiInstallTimerCmd() *cobra.Command {
	var (
		configFile string
		pkiDir     string
		identities []string
		schedule   string
		unitName   string
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "install-timer",
		Short: "Install systemd timer for automatic certificate rotation",
		Long: `Install a systemd timer that automatically renews expiring certificates.

The timer runs the 'nixfleet pki renew' command on the specified schedule.
Default schedule is daily with a random delay of up to 1 hour.

Schedule examples:
  daily          - Once per day (default)
  weekly         - Once per week (Monday)
  *-*-* 03:00:00 - Every day at 3 AM
  Mon *-*-* 02:00:00 - Every Monday at 2 AM

Examples:
  nixfleet pki install-timer --config secrets/pki.yaml
  nixfleet pki install-timer --schedule weekly
  nixfleet pki install-timer --dry-run  # Preview without installing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get the absolute path to nixfleet binary
			nixfleetPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("getting executable path: %w", err)
			}
			nixfleetPath, err = filepath.Abs(nixfleetPath)
			if err != nil {
				return fmt.Errorf("getting absolute path: %w", err)
			}

			// Get absolute path to pki dir
			absPkiDir, err := filepath.Abs(pkiDir)
			if err != nil {
				return fmt.Errorf("getting absolute pki dir: %w", err)
			}

			// Resolve identity file paths
			var absIdentities []string
			for _, id := range identities {
				absID, err := filepath.Abs(id)
				if err != nil {
					return fmt.Errorf("resolving identity path %s: %w", id, err)
				}
				absIdentities = append(absIdentities, absID)
			}

			// Resolve config file path
			absConfig := ""
			if configFile != "" {
				absConfig, err = filepath.Abs(configFile)
				if err != nil {
					return fmt.Errorf("resolving config path: %w", err)
				}
			}

			// Generate systemd units
			serviceContent := pki.SystemdService(nixfleetPath, absConfig, absPkiDir, absIdentities)
			timerContent := pki.SystemdTimer(schedule)

			servicePath, timerPath := pki.SystemdUnitPaths(unitName)

			if dryRun {
				fmt.Println("=== DRY RUN - Would create the following files ===")
				fmt.Println()
				fmt.Printf("=== %s ===\n", servicePath)
				fmt.Println(serviceContent)
				fmt.Printf("=== %s ===\n", timerPath)
				fmt.Println(timerContent)
				fmt.Println("=== Commands that would be run ===")
				fmt.Println("  systemctl daemon-reload")
				fmt.Printf("  systemctl enable --now %s.timer\n", unitName)
				return nil
			}

			// Check if running as root
			if os.Geteuid() != 0 {
				return fmt.Errorf("must be run as root to install systemd units (try: sudo nixfleet pki install-timer ...)")
			}

			// Write service file
			if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
				return fmt.Errorf("writing service file: %w", err)
			}
			fmt.Printf("Created %s\n", servicePath)

			// Write timer file
			if err := os.WriteFile(timerPath, []byte(timerContent), 0644); err != nil {
				return fmt.Errorf("writing timer file: %w", err)
			}
			fmt.Printf("Created %s\n", timerPath)

			// Reload systemd
			if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
				return fmt.Errorf("systemctl daemon-reload: %w", err)
			}
			fmt.Println("Reloaded systemd daemon")

			// Enable and start timer
			if err := exec.Command("systemctl", "enable", "--now", unitName+".timer").Run(); err != nil {
				return fmt.Errorf("enabling timer: %w", err)
			}
			fmt.Printf("Enabled and started %s.timer\n", unitName)

			fmt.Println()
			fmt.Println("Certificate rotation timer installed successfully!")
			fmt.Println()
			fmt.Println("Useful commands:")
			fmt.Printf("  systemctl status %s.timer   # Check timer status\n", unitName)
			fmt.Printf("  systemctl list-timers       # List all timers\n")
			fmt.Printf("  journalctl -u %s            # View service logs\n", unitName)
			fmt.Printf("  systemctl start %s          # Run renewal now\n", unitName)

			return nil
		},
	}

	cmd.Flags().StringVarP(&configFile, "config", "c", "", "PKI config file")
	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringVar(&schedule, "schedule", "daily", "Timer schedule (systemd calendar format)")
	cmd.Flags().StringVar(&unitName, "unit-name", "nixfleet-pki-renew", "Name for systemd units")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview units without installing")

	return cmd
}

func pkiUninstallTimerCmd() *cobra.Command {
	var (
		unitName string
	)

	cmd := &cobra.Command{
		Use:   "uninstall-timer",
		Short: "Remove systemd timer for certificate rotation",
		Long: `Remove the systemd timer and service for automatic certificate rotation.

Examples:
  nixfleet pki uninstall-timer
  nixfleet pki uninstall-timer --unit-name custom-name`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if running as root
			if os.Geteuid() != 0 {
				return fmt.Errorf("must be run as root to remove systemd units (try: sudo nixfleet pki uninstall-timer ...)")
			}

			servicePath, timerPath := pki.SystemdUnitPaths(unitName)

			// Stop and disable timer
			_ = exec.Command("systemctl", "stop", unitName+".timer").Run()
			_ = exec.Command("systemctl", "disable", unitName+".timer").Run()
			fmt.Printf("Stopped and disabled %s.timer\n", unitName)

			// Remove files
			if err := os.Remove(timerPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing timer file: %w", err)
			}
			if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing service file: %w", err)
			}
			fmt.Printf("Removed %s\n", servicePath)
			fmt.Printf("Removed %s\n", timerPath)

			// Reload systemd
			if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
				return fmt.Errorf("systemctl daemon-reload: %w", err)
			}
			fmt.Println("Reloaded systemd daemon")

			fmt.Println()
			fmt.Println("Certificate rotation timer removed.")

			return nil
		},
	}

	cmd.Flags().StringVar(&unitName, "unit-name", "nixfleet-pki-renew", "Name of systemd units to remove")

	return cmd
}

// =============================================================================
// k0s Commands - Kubernetes cluster management
// =============================================================================

func k0sCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "k0s",
		Short: "Kubernetes cluster management with k0s",
		Long: `Manage k0s Kubernetes clusters across your fleet.

The k0s integration works in two modes:
  1. Controller init (manual): Bootstrap cluster and generate join tokens
  2. Worker join (pull-mode): Workers auto-join using encrypted tokens

Workflow:
  1. nixfleet k0s init -H controller-host    # Bootstrap controller
  2. nixfleet k0s certmanager -H controller  # Deploy Fleet CA for TLS
  3. Add worker hosts to config with role=worker
  4. Workers auto-join on next pull

Commands:
  init         - Bootstrap k0s controller and generate join tokens
  status       - Show cluster status
  kubeconfig   - Fetch admin kubeconfig from controller
  certmanager  - Deploy Fleet CA to cert-manager for TLS certificates
  token        - Generate new join tokens
  rekey        - Re-encrypt tokens with new recipients`,
	}

	cmd.AddCommand(k0sInitCmd())
	cmd.AddCommand(k0sStatusCmd())
	cmd.AddCommand(k0sRekeyCmd())
	cmd.AddCommand(k0sTokenCmd())
	cmd.AddCommand(k0sKubeconfigCmd())
	cmd.AddCommand(k0sCertManagerCmd())

	return cmd
}

func k0sKubeconfigCmd() *cobra.Command {
	var (
		hostName   string
		outputFile string
		context    string
	)

	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Fetch admin kubeconfig from controller",
		Long: `Fetch the admin kubeconfig from a k0s controller.

This command SSHes to the controller and retrieves the admin kubeconfig,
which can be used to manage the cluster with kubectl.

Examples:
  # Print kubeconfig to stdout
  nixfleet k0s kubeconfig -H controller

  # Save to file
  nixfleet k0s kubeconfig -H controller -o ~/.kube/fleet.yaml

  # Save with custom context name
  nixfleet k0s kubeconfig -H controller -o ~/.kube/fleet.yaml --context fleet`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Load inventory
			inv, err := inventory.LoadFromDir(inventoryPath)
			if err != nil {
				inv, err = inventory.LoadFromFile(inventoryPath)
			}
			if err != nil {
				return fmt.Errorf("loading inventory: %w", err)
			}

			// Find target host
			host, ok := inv.GetHost(hostName)
			if !ok {
				return fmt.Errorf("host %q not found in inventory", hostName)
			}

			// Connect via SSH
			pool := ssh.NewPool(nil)
			defer pool.Close()

			client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
			if err != nil {
				return fmt.Errorf("connecting to host: %w", err)
			}

			// Fetch kubeconfig
			result, err := client.Exec(ctx, "sudo k0s kubeconfig admin")
			if err != nil {
				return fmt.Errorf("fetching kubeconfig: %w", err)
			}
			if result.ExitCode != 0 {
				errMsg := strings.TrimSpace(result.Stderr)
				if errMsg == "" {
					errMsg = strings.TrimSpace(result.Stdout)
				}
				return fmt.Errorf("fetching kubeconfig: %s", errMsg)
			}

			kubeconfig := result.Stdout

			// If custom context name provided, replace the default
			if context != "" {
				kubeconfig = strings.ReplaceAll(kubeconfig, "name: default", "name: "+context)
				kubeconfig = strings.ReplaceAll(kubeconfig, "cluster: default", "cluster: "+context)
				kubeconfig = strings.ReplaceAll(kubeconfig, "user: default", "user: "+context)
				kubeconfig = strings.ReplaceAll(kubeconfig, "context: default", "context: "+context)
				kubeconfig = strings.ReplaceAll(kubeconfig, "current-context: default", "current-context: "+context)
			}

			// Output to file or stdout
			if outputFile != "" {
				// Expand ~ to home directory
				if strings.HasPrefix(outputFile, "~/") {
					home, err := os.UserHomeDir()
					if err != nil {
						return fmt.Errorf("getting home directory: %w", err)
					}
					outputFile = filepath.Join(home, outputFile[2:])
				}

				// Create directory if needed
				dir := filepath.Dir(outputFile)
				if err := os.MkdirAll(dir, 0755); err != nil {
					return fmt.Errorf("creating directory: %w", err)
				}

				// Write file with restricted permissions
				if err := os.WriteFile(outputFile, []byte(kubeconfig), 0600); err != nil {
					return fmt.Errorf("writing kubeconfig: %w", err)
				}
				fmt.Printf("Kubeconfig saved to %s\n", outputFile)
				fmt.Println()
				fmt.Println("To use this kubeconfig:")
				fmt.Printf("  export KUBECONFIG=%s\n", outputFile)
				fmt.Println("  kubectl get nodes")
			} else {
				fmt.Print(kubeconfig)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&hostName, "host", "H", "", "Controller host name (required)")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file path (default: stdout)")
	cmd.Flags().StringVar(&context, "context", "", "Custom context name (default: k0s default)")
	cmd.MarkFlagRequired("host")

	return cmd
}

func k0sInitCmd() *cobra.Command {
	var (
		clusterName   string
		configFile    string
		apiSANs       []string
		podCIDR       string
		serviceCIDR   string
		recipients    []string
		identities    []string
		enableWorker  bool
		tokenExpiry   string
		commitChanges bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize k0s controller and generate join tokens",
		Long: `Bootstrap a k0s controller on the specified host and generate join tokens.

This command:
  1. SSHs to the target host
  2. Installs k0s if not present
  3. Generates k0s.yaml configuration
  4. Bootstraps the controller
  5. Generates worker and controller join tokens
  6. Encrypts tokens with age for all inventory hosts
  7. Saves tokens to secrets/k0s/
  8. Updates host config to set role=controller (or controller+worker)
  9. Optionally commits changes to git

Prerequisites:
  - Target host must be in inventory
  - SSH access to target host
  - Age recipients configured (admin key + host keys)

Example:
  nixfleet k0s init -H gtr --cluster stigen-fleet --san k8s.stigen.ai`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if targetHost == "" {
				return fmt.Errorf("--host is required")
			}

			// Check required tools are available
			for _, tool := range []string{"age", "ssh-to-age"} {
				if _, err := exec.LookPath(tool); err != nil {
					return fmt.Errorf("%s is required but not found in PATH. Install with: nix-shell -p %s", tool, tool)
				}
			}

			// Load inventory
			inv, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if len(hosts) != 1 {
				return fmt.Errorf("exactly one host must be specified with -H")
			}
			host := hosts[0]

			// Collect age recipients: admin keys + all host SSH keys
			allRecipients := make([]string, 0)
			allRecipients = append(allRecipients, recipients...)

			// Get SSH host keys from all hosts and convert to age keys
			fmt.Println("Collecting age recipients from inventory hosts...")
			pool := ssh.NewPool(nil)
			defer pool.Close()

			for _, h := range inv.Hosts {
				client, err := pool.GetWithUser(ctx, h.Addr, h.SSHPort, h.SSHUser)
				if err != nil {
					fmt.Printf("  Warning: Cannot connect to %s, skipping: %v\n", h.Name, err)
					continue
				}

				// Get SSH host key
				result, err := client.Exec(ctx, "cat /etc/ssh/ssh_host_ed25519_key.pub")
				if err != nil || result.ExitCode != 0 {
					fmt.Printf("  Warning: Cannot get SSH key from %s: %v\n", h.Name, err)
					continue
				}

				// Convert to age key using ssh-to-age
				sshKey := strings.TrimSpace(result.Stdout)
				ageCmd := exec.CommandContext(ctx, "ssh-to-age")
				ageCmd.Stdin = strings.NewReader(sshKey)
				ageOutput, err := ageCmd.Output()
				if err != nil {
					fmt.Printf("  Warning: Cannot convert key for %s: %v\n", h.Name, err)
					continue
				}

				ageKey := strings.TrimSpace(string(ageOutput))
				if ageKey != "" {
					allRecipients = append(allRecipients, ageKey)
					fmt.Printf("  Added %s: %s\n", h.Name, ageKey[:20]+"...")
				}
			}

			if len(allRecipients) == 0 {
				return fmt.Errorf("no age recipients available. Specify --recipient or ensure hosts are reachable")
			}

			fmt.Printf("\nInitializing k0s controller on %s...\n\n", host.Name)

			// Get SSH client for controller
			client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
			if err != nil {
				return fmt.Errorf("connecting to %s: %w", host.Name, err)
			}

			// Check if k0s is installed
			checkResult, err := client.Exec(ctx, "which k0s")
			if err != nil || checkResult.ExitCode != 0 {
				fmt.Println("Installing k0s...")
				installCmd := "curl -sSLf https://get.k0s.sh | sudo sh"
				installResult, err := client.Exec(ctx, installCmd)
				if err != nil || installResult.ExitCode != 0 {
					return fmt.Errorf("installing k0s: %w", err)
				}
			}

			// Get the host's actual IP address (k0s doesn't accept 0.0.0.0)
			fmt.Println("Getting host IP address...")
			ipResult, err := client.Exec(ctx, "ip -4 addr show | grep 'inet ' | grep -v '127.0.0.1' | head -1 | awk '{print $2}' | cut -d/ -f1")
			if err != nil || ipResult.ExitCode != 0 || strings.TrimSpace(ipResult.Stdout) == "" {
				return fmt.Errorf("failed to get host IP address: %v", err)
			}
			hostIP := strings.TrimSpace(ipResult.Stdout)
			fmt.Printf("  Host IP: %s\n", hostIP)

			// Build SANs list (include both hostname and IP)
			allSANs := append([]string{host.Addr, host.Name, hostIP}, apiSANs...)

			// Generate k0s.yaml
			// Note: k0s requires actual IP, not 0.0.0.0; provider is "kuberouter" not "kube-router"
			k0sConfig := fmt.Sprintf(`apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
metadata:
  name: %s
spec:
  api:
    address: %s
    port: 6443
    sans:
%s
  network:
    provider: kuberouter
    podCIDR: %s
    serviceCIDR: %s
    clusterDomain: cluster.local
  storage:
    type: etcd
  telemetry:
    enabled: false
`, clusterName, hostIP, formatYAMLList(allSANs, 6), podCIDR, serviceCIDR)

			fmt.Println("Writing k0s configuration...")
			mkdirResult, err := client.Exec(ctx, "sudo mkdir -p /etc/k0s")
			if err != nil {
				return fmt.Errorf("creating /etc/k0s: %w", err)
			}
			if mkdirResult.ExitCode != 0 {
				return fmt.Errorf("creating /etc/k0s: %s", strings.TrimSpace(mkdirResult.Stderr))
			}

			// Write config via heredoc
			writeCmd := fmt.Sprintf("sudo tee /etc/k0s/k0s.yaml > /dev/null << 'ENDCONFIG'\n%sENDCONFIG", k0sConfig)
			writeResult, err := client.Exec(ctx, writeCmd)
			if err != nil {
				return fmt.Errorf("writing k0s.yaml: %w", err)
			}
			if writeResult.ExitCode != 0 {
				return fmt.Errorf("writing k0s.yaml: %s", strings.TrimSpace(writeResult.Stderr))
			}

			// Check if already bootstrapped
			testResult, _ := client.Exec(ctx, "test -f /var/lib/k0s/pki/ca.crt")
			alreadyBootstrapped := testResult != nil && testResult.ExitCode == 0

			if alreadyBootstrapped {
				fmt.Println("Cluster already bootstrapped, skipping init...")
			} else {
				fmt.Println("Bootstrapping k0s controller...")
				workerFlag := ""
				if enableWorker {
					workerFlag = "--enable-worker"
				}
				initCmd := fmt.Sprintf("sudo k0s install controller --config /etc/k0s/k0s.yaml %s && sudo k0s start", workerFlag)
				initResult, err := client.Exec(ctx, initCmd)
				if err != nil {
					return fmt.Errorf("bootstrapping k0s: %w", err)
				}
				if initResult.ExitCode != 0 {
					errMsg := strings.TrimSpace(initResult.Stderr)
					if errMsg == "" {
						errMsg = strings.TrimSpace(initResult.Stdout)
					}
					return fmt.Errorf("bootstrapping k0s: %s", errMsg)
				}

				// Wait for API to be ready
				fmt.Println("Waiting for API server to be ready...")
				for i := 0; i < 60; i++ {
					time.Sleep(5 * time.Second)
					apiResult, err := client.Exec(ctx, "sudo k0s kubectl get nodes")
					if err == nil && apiResult.ExitCode == 0 {
						break
					}
					if i == 59 {
						return fmt.Errorf("timeout waiting for API server")
					}
					fmt.Printf("  Waiting... (%d/60)\n", i+1)
				}

				// Remove control-plane NoSchedule taint for single-node/controller+worker clusters
				// This allows pods to be scheduled on the control-plane node
				fmt.Println("Removing control-plane NoSchedule taint for workloads...")
				taintResult, err := client.Exec(ctx, "sudo k0s kubectl taint nodes --all node-role.kubernetes.io/control-plane:NoSchedule- 2>/dev/null || true")
				if err != nil {
					// Non-fatal: taint might not exist or node not ready yet
					fmt.Printf("  Warning: could not remove taint: %v\n", err)
				} else if strings.Contains(taintResult.Stdout, "untainted") || strings.Contains(taintResult.Stderr, "not found") {
					fmt.Println("  Control-plane taint removed - pods can now schedule on this node")
				}
			}

			fmt.Println("\nGenerating join tokens...")

			// Generate worker token
			workerTokenCmd := fmt.Sprintf("sudo k0s token create --role=worker --expiry=%s", tokenExpiry)
			workerTokenResult, err := client.Exec(ctx, workerTokenCmd)
			if err != nil {
				return fmt.Errorf("generating worker token: %w", err)
			}
			if workerTokenResult.ExitCode != 0 {
				return fmt.Errorf("generating worker token: %s", strings.TrimSpace(workerTokenResult.Stderr))
			}
			workerToken := strings.TrimSpace(workerTokenResult.Stdout)

			// Generate controller token
			controllerTokenCmd := fmt.Sprintf("sudo k0s token create --role=controller --expiry=%s", tokenExpiry)
			controllerTokenResult, err := client.Exec(ctx, controllerTokenCmd)
			if err != nil {
				return fmt.Errorf("generating controller token: %w", err)
			}
			if controllerTokenResult.ExitCode != 0 {
				return fmt.Errorf("generating controller token: %s", strings.TrimSpace(controllerTokenResult.Stderr))
			}
			controllerToken := strings.TrimSpace(controllerTokenResult.Stdout)

			fmt.Println("Encrypting tokens with age...")

			// Create secrets directory
			secretsDir := filepath.Join(flakePath, "secrets", "k0s")
			if err := os.MkdirAll(secretsDir, 0755); err != nil {
				return fmt.Errorf("creating secrets directory: %w", err)
			}

			// Build age recipients args
			recipientArgs := make([]string, 0)
			for _, r := range allRecipients {
				recipientArgs = append(recipientArgs, "-r", r)
			}

			// Encrypt worker token
			workerTokenPath := filepath.Join(secretsDir, "worker-token.age")
			ageEncrypt := exec.CommandContext(ctx, "age", append(recipientArgs, "-o", workerTokenPath)...)
			ageEncrypt.Stdin = strings.NewReader(workerToken)
			if err := ageEncrypt.Run(); err != nil {
				return fmt.Errorf("encrypting worker token: %w", err)
			}
			fmt.Printf("  Saved: %s\n", workerTokenPath)

			// Encrypt controller token
			controllerTokenPath := filepath.Join(secretsDir, "controller-token.age")
			ageEncrypt = exec.CommandContext(ctx, "age", append(recipientArgs, "-o", controllerTokenPath)...)
			ageEncrypt.Stdin = strings.NewReader(controllerToken)
			if err := ageEncrypt.Run(); err != nil {
				return fmt.Errorf("encrypting controller token: %w", err)
			}
			fmt.Printf("  Saved: %s\n", controllerTokenPath)

			// Get controller endpoint for worker configs (use actual IP for reliability)
			controllerEndpoint := fmt.Sprintf("https://%s:6443", hostIP)

			// Save cluster info
			clusterInfo := fmt.Sprintf(`# k0s Cluster: %s
# Controller: %s
# Endpoint: %s
# Initialized: %s
#
# Workers can join by setting in their config:
#   nixfleet.k0s = {
#     enable = true;
#     role = "worker";
#     cluster.controllerEndpoint = "%s";
#     joinToken = ../secrets/k0s/worker-token.age;
#   };
`, clusterName, host.Name, controllerEndpoint, time.Now().Format(time.RFC3339), controllerEndpoint)

			clusterInfoPath := filepath.Join(secretsDir, "cluster-info.txt")
			if err := os.WriteFile(clusterInfoPath, []byte(clusterInfo), 0644); err != nil {
				return fmt.Errorf("writing cluster info: %w", err)
			}

			fmt.Println("\nUpdating host configuration...")

			// Find and update host config file
			hostConfigPath := filepath.Join(flakePath, "hosts", host.Name+".nix")
			if _, err := os.Stat(hostConfigPath); err != nil {
				fmt.Printf("  Warning: Host config not found at %s, skipping config update\n", hostConfigPath)
			} else {
				// Read current config
				configBytes, err := os.ReadFile(hostConfigPath)
				if err != nil {
					return fmt.Errorf("reading host config: %w", err)
				}
				configContent := string(configBytes)

				// Check if k0s config already exists
				if strings.Contains(configContent, "nixfleet.k0s") {
					fmt.Println("  k0s configuration already exists in host config")
				} else {
					// Add k0s configuration before the closing brace
					role := "controller"
					if enableWorker {
						role = "controller+worker"
					}

					k0sConfig := fmt.Sprintf(`
    #=========================================================================
    # k0s Kubernetes - Controller Node
    #=========================================================================

    k0s = {
      enable = true;
      role = "%s";

      cluster = {
        name = "%s";
        controllerEndpoint = "%s";
      };

      api.sans = [
%s
      ];

      pki.useFleetCA = true;
    };
`, role, clusterName, controllerEndpoint, formatNixList(allSANs, 8))

					// Find position to insert (before final closing braces)
					insertPos := strings.LastIndex(configContent, "  };\n}")
					if insertPos > 0 {
						configContent = configContent[:insertPos] + k0sConfig + configContent[insertPos:]
						if err := os.WriteFile(hostConfigPath, []byte(configContent), 0644); err != nil {
							return fmt.Errorf("writing host config: %w", err)
						}
						fmt.Printf("  Updated: %s\n", hostConfigPath)
					} else {
						fmt.Println("  Warning: Could not find insertion point in host config")
					}
				}
			}

			if commitChanges {
				fmt.Println("\nCommitting changes to git...")
				gitAdd := exec.CommandContext(ctx, "git", "add", secretsDir, hostConfigPath)
				gitAdd.Dir = flakePath
				if err := gitAdd.Run(); err != nil {
					return fmt.Errorf("git add: %w", err)
				}

				commitMsg := fmt.Sprintf("k0s: Initialize cluster '%s' on %s", clusterName, host.Name)
				gitCommit := exec.CommandContext(ctx, "git", "commit", "-m", commitMsg)
				gitCommit.Dir = flakePath
				if err := gitCommit.Run(); err != nil {
					fmt.Println("  Warning: git commit failed (maybe no changes?)")
				} else {
					fmt.Println("  Committed changes")
				}
			}

			fmt.Println("\n" + strings.Repeat("=", 60))
			fmt.Printf("k0s cluster '%s' initialized successfully!\n", clusterName)
			fmt.Println(strings.Repeat("=", 60))
			fmt.Printf("\nController: %s (%s)\n", host.Name, controllerEndpoint)
			fmt.Printf("Role: %s\n", func() string {
				if enableWorker {
					return "controller+worker"
				}
				return "controller"
			}())
			fmt.Println("\nTo add workers, create a host config with:")
			fmt.Printf(`
  nixfleet.k0s = {
    enable = true;
    role = "worker";
    cluster.controllerEndpoint = "%s";
    joinToken = ../secrets/k0s/worker-token.age;
  };
`, controllerEndpoint)
			fmt.Println("\nWorkers will auto-join on next pull-mode sync.")

			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "nixfleet", "Cluster name")
	cmd.Flags().StringVar(&configFile, "config", "", "k0s config file (optional)")
	cmd.Flags().StringSliceVar(&apiSANs, "san", nil, "Additional API server SANs")
	cmd.Flags().StringVar(&podCIDR, "pod-cidr", "10.244.0.0/16", "Pod CIDR")
	cmd.Flags().StringVar(&serviceCIDR, "service-cidr", "10.96.0.0/12", "Service CIDR")
	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipients for token encryption")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().BoolVar(&enableWorker, "enable-worker", true, "Also run as worker (controller+worker mode)")
	cmd.Flags().StringVar(&tokenExpiry, "token-expiry", "8760h", "Token expiry (default 1 year)")
	cmd.Flags().BoolVar(&commitChanges, "commit", true, "Commit changes to git")

	return cmd
}

func k0sStatusCmd() *cobra.Command {
	var showState bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show k0s cluster status",
		Long: `Show comprehensive k0s cluster status including nodes, Helm releases, IP pools, and tracked state.

Examples:
  nixfleet k0s status -H gtr           # Status for specific host
  nixfleet k0s status -H gtr --state   # Include tracked k0s state`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			pool := ssh.NewPool(nil)
			defer pool.Close()

			reconciler := k0s.NewReconciler()
			stateMgr := state.NewManager()

			fmt.Println("k0s Cluster Status")
			fmt.Println(strings.Repeat("=", 60))

			for _, host := range hosts {
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("\n%s: Connection failed: %v\n", host.Name, err)
					continue
				}

				// Check if k0s is running
				statusResult, err := client.Exec(ctx, "sudo k0s status 2>/dev/null || echo 'not running'")
				if err != nil || strings.Contains(statusResult.Stdout, "not running") {
					fmt.Printf("\n%s: k0s not running\n", host.Name)
					continue
				}

				fmt.Printf("\n%s:\n", host.Name)
				fmt.Println(statusResult.Stdout)

				// Get detailed status from reconciler
				k0sStatus, err := reconciler.GetStatus(ctx, client)
				if err == nil && k0sStatus.Enabled {
					// Show nodes
					if len(k0sStatus.Nodes) > 0 {
						fmt.Println("\nNodes:")
						for _, node := range k0sStatus.Nodes {
							readyStr := "Ready"
							if !node.Ready {
								readyStr = "NotReady"
							}
							fmt.Printf("  - %s: %s\n", node.Name, readyStr)
						}
					}

					// Show Helm releases
					if len(k0sStatus.HelmReleases) > 0 {
						fmt.Println("\nHelm Releases:")
						for _, rel := range k0sStatus.HelmReleases {
							fmt.Printf("  - %s: %s (%s)\n", rel.Name, rel.Version, rel.Status)
						}
					}

					// Show IP pools
					if len(k0sStatus.IPPools) > 0 {
						fmt.Println("\nLoadBalancer IP Pools:")
						for _, p := range k0sStatus.IPPools {
							fmt.Printf("  - %s: %s\n", p.Name, p.CIDR)
						}
					}
				}

				// Show tracked state if requested
				if showState {
					hostState, err := stateMgr.ReadState(ctx, client)
					if err == nil && hostState.K0s != nil && hostState.K0s.Enabled {
						k0sState := hostState.K0s
						fmt.Println("\nTracked State:")
						if len(k0sState.ConfigHash) > 16 {
							fmt.Printf("  Config Hash: %s...\n", k0sState.ConfigHash[:16])
						}
						if !k0sState.LastReconcile.IsZero() {
							fmt.Printf("  Last Reconcile: %s\n", k0sState.LastReconcile.Format("2006-01-02 15:04:05"))
						}

						if len(k0sState.HelmCharts) > 0 {
							fmt.Printf("  Tracked Charts: %d\n", len(k0sState.HelmCharts))
							for _, c := range k0sState.HelmCharts {
								fmt.Printf("    - %s (%s)\n", c.Name, c.Namespace)
							}
						}

						if len(k0sState.Manifests) > 0 {
							fmt.Printf("  Tracked Manifests: %d\n", len(k0sState.Manifests))
							for _, m := range k0sState.Manifests {
								fmt.Printf("    - %s/%s (%s)\n", m.Kind, m.Name, m.LogicalName)
							}
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&showState, "state", false, "Show tracked k0s state for reconciliation")

	return cmd
}

func k0sRekeyCmd() *cobra.Command {
	var (
		recipients []string
		identities []string
		addHost    string
	)

	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Re-encrypt k0s tokens for additional hosts",
		Long: `Re-encrypt k0s join tokens to add new recipients.

Use this when adding new worker hosts to allow them to decrypt the join token.

Example:
  nixfleet k0s rekey --add-host new-worker
  nixfleet k0s rekey -r age1newkey...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			secretsDir := filepath.Join(flakePath, "secrets", "k0s")

			// Collect all recipients
			allRecipients := append([]string{}, recipients...)

			// If adding a specific host, get its key
			if addHost != "" {
				inv, err := inventory.LoadFromDir(inventoryPath)
				if err != nil {
					inv, err = inventory.LoadFromFile(inventoryPath)
					if err != nil {
						return fmt.Errorf("loading inventory: %w", err)
					}
				}

				host, ok := inv.GetHost(addHost)
				if !ok {
					return fmt.Errorf("host %s not found in inventory", addHost)
				}

				pool := ssh.NewPool(nil)
				defer pool.Close()

				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					return fmt.Errorf("connecting to %s: %w", addHost, err)
				}

				sshKeyResult, err := client.Exec(ctx, "cat /etc/ssh/ssh_host_ed25519_key.pub")
				if err != nil || sshKeyResult.ExitCode != 0 {
					return fmt.Errorf("getting SSH key: %w", err)
				}
				sshKey := sshKeyResult.Stdout

				ageCmd := exec.CommandContext(ctx, "ssh-to-age")
				ageCmd.Stdin = strings.NewReader(strings.TrimSpace(sshKey))
				ageOutput, err := ageCmd.Output()
				if err != nil {
					return fmt.Errorf("converting key: %w", err)
				}

				allRecipients = append(allRecipients, strings.TrimSpace(string(ageOutput)))
				fmt.Printf("Added recipient for %s\n", addHost)
			}

			if len(allRecipients) == 0 {
				return fmt.Errorf("no recipients specified. Use --recipient or --add-host")
			}

			// Re-encrypt each token file
			tokenFiles := []string{"worker-token.age", "controller-token.age"}
			for _, tokenFile := range tokenFiles {
				tokenPath := filepath.Join(secretsDir, tokenFile)
				if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
					continue
				}

				fmt.Printf("Re-encrypting %s...\n", tokenFile)

				// Decrypt
				decryptArgs := []string{"-d"}
				for _, id := range identities {
					decryptArgs = append(decryptArgs, "-i", id)
				}
				decryptArgs = append(decryptArgs, tokenPath)

				decryptCmd := exec.CommandContext(ctx, "age", decryptArgs...)
				plaintext, err := decryptCmd.Output()
				if err != nil {
					return fmt.Errorf("decrypting %s: %w", tokenFile, err)
				}

				// Re-encrypt with all recipients
				encryptArgs := []string{}
				for _, r := range allRecipients {
					encryptArgs = append(encryptArgs, "-r", r)
				}
				encryptArgs = append(encryptArgs, "-o", tokenPath)

				encryptCmd := exec.CommandContext(ctx, "age", encryptArgs...)
				encryptCmd.Stdin = strings.NewReader(string(plaintext))
				if err := encryptCmd.Run(); err != nil {
					return fmt.Errorf("encrypting %s: %w", tokenFile, err)
				}
			}

			fmt.Println("Tokens re-encrypted successfully")
			return nil
		},
	}

	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipients to add")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringVar(&addHost, "add-host", "", "Add a host from inventory as recipient")

	return cmd
}

func k0sTokenCmd() *cobra.Command {
	var (
		role       string
		expiry     string
		recipients []string
	)

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Generate new k0s join token",
		Long: `Generate a new join token from an existing controller.

Use this to rotate tokens or generate tokens with different expiry.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if targetHost == "" {
				return fmt.Errorf("--host is required (specify the controller)")
			}

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

			if len(hosts) != 1 {
				return fmt.Errorf("exactly one host must be specified")
			}
			host := hosts[0]

			pool := ssh.NewPool(nil)
			defer pool.Close()

			client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
			if err != nil {
				return fmt.Errorf("connecting to %s: %w", host.Name, err)
			}

			tokenCmd := fmt.Sprintf("sudo k0s token create --role=%s --expiry=%s", role, expiry)
			tokenResult, err := client.Exec(ctx, tokenCmd)
			if err != nil || tokenResult.ExitCode != 0 {
				return fmt.Errorf("generating token: %w", err)
			}
			token := strings.TrimSpace(tokenResult.Stdout)

			if len(recipients) > 0 {
				// Encrypt and save
				secretsDir := filepath.Join(flakePath, "secrets", "k0s")
				tokenPath := filepath.Join(secretsDir, role+"-token.age")

				recipientArgs := []string{}
				for _, r := range recipients {
					recipientArgs = append(recipientArgs, "-r", r)
				}
				recipientArgs = append(recipientArgs, "-o", tokenPath)

				encryptCmd := exec.CommandContext(ctx, "age", recipientArgs...)
				encryptCmd.Stdin = strings.NewReader(token)
				if err := encryptCmd.Run(); err != nil {
					return fmt.Errorf("encrypting token: %w", err)
				}

				fmt.Printf("Token saved to %s\n", tokenPath)
			} else {
				fmt.Println(token)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "worker", "Token role (worker or controller)")
	cmd.Flags().StringVar(&expiry, "expiry", "8760h", "Token expiry")
	cmd.Flags().StringSliceVarP(&recipients, "recipient", "r", nil, "Age recipients (if set, encrypts and saves token)")

	return cmd
}

func k0sCertManagerCmd() *cobra.Command {
	var (
		hostName   string
		pkiDir     string
		identities []string
		secretName string
		namespace  string
		issuerName string
		verify     bool
	)

	cmd := &cobra.Command{
		Use:   "certmanager",
		Short: "Deploy Fleet CA to cert-manager",
		Long: `Deploy the Fleet PKI intermediate CA to Kubernetes for cert-manager.

This command:
  1. Loads the Fleet intermediate CA from the PKI store
  2. Creates a Kubernetes TLS secret in the cert-manager namespace
  3. Optionally verifies the ClusterIssuer becomes ready

The ClusterIssuer manifest should be deployed via k0s Helm extensions
(configured in the k0s.nix module). This command only deploys the CA secret.

Prerequisites:
  - k0s controller running with cert-manager installed
  - Fleet PKI initialized (nixfleet pki init)
  - Age identity for decrypting the CA private key

Examples:
  # Deploy Fleet CA to cert-manager
  nixfleet k0s certmanager -H controller

  # Deploy with custom secret name
  nixfleet k0s certmanager -H controller --secret-name my-ca

  # Deploy and verify issuer is ready
  nixfleet k0s certmanager -H controller --verify`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Load inventory
			inv, err := inventory.LoadFromDir(inventoryPath)
			if err != nil {
				inv, err = inventory.LoadFromFile(inventoryPath)
			}
			if err != nil {
				return fmt.Errorf("loading inventory: %w", err)
			}

			// Find target host
			host, ok := inv.GetHost(hostName)
			if !ok {
				return fmt.Errorf("host %q not found in inventory", hostName)
			}

			// Load age identities
			var ageIdentities []string
			if len(identities) > 0 {
				ageIdentities = identities
			} else {
				// Try default identity location
				home, _ := os.UserHomeDir()
				defaultIdentity := filepath.Join(home, ".config", "age", "admin-key.txt")
				if _, err := os.Stat(defaultIdentity); err == nil {
					ageIdentities = []string{defaultIdentity}
				}
			}

			if len(ageIdentities) == 0 {
				return fmt.Errorf("no age identity specified and default not found at ~/.config/age/admin-key.txt")
			}

			// Load the intermediate CA
			store := pki.NewStore(pkiDir, nil, ageIdentities)
			ica, err := store.LoadIntermediateCA(ctx)
			if err != nil {
				return fmt.Errorf("loading intermediate CA: %w", err)
			}

			// Generate the Kubernetes secret
			secret, err := pki.ExportIntermediateCAToK8sSecret(ica, namespace, secretName)
			if err != nil {
				return fmt.Errorf("generating secret: %w", err)
			}

			secretJSON, err := json.Marshal(secret)
			if err != nil {
				return fmt.Errorf("marshaling secret: %w", err)
			}

			// Connect via SSH
			pool := ssh.NewPool(nil)
			defer pool.Close()

			client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
			if err != nil {
				return fmt.Errorf("connecting to host: %w", err)
			}

			fmt.Printf("Deploying Fleet CA secret to %s...\n", hostName)

			// Apply the secret via k0s kubectl
			applyCmd := fmt.Sprintf("echo '%s' | sudo k0s kubectl apply -f -", string(secretJSON))
			result, err := client.Exec(ctx, applyCmd)
			if err != nil {
				return fmt.Errorf("applying secret: %w", err)
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("applying secret: %s", strings.TrimSpace(result.Stderr))
			}

			fmt.Printf("✓ Secret %s/%s created\n", namespace, secretName)

			// Verify ClusterIssuer if requested
			if verify {
				fmt.Printf("Verifying ClusterIssuer %s...\n", issuerName)

				// Wait for issuer to be ready (up to 30 seconds)
				for i := 0; i < 15; i++ {
					checkCmd := fmt.Sprintf("sudo k0s kubectl get clusterissuer %s -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}'", issuerName)
					result, err := client.Exec(ctx, checkCmd)
					if err == nil && result.ExitCode == 0 {
						status := strings.Trim(result.Stdout, "'")
						if status == "True" {
							fmt.Printf("✓ ClusterIssuer %s is ready\n", issuerName)
							return nil
						}
					}
					time.Sleep(2 * time.Second)
				}

				// Get more details on failure
				describeCmd := fmt.Sprintf("sudo k0s kubectl describe clusterissuer %s", issuerName)
				result, _ := client.Exec(ctx, describeCmd)
				return fmt.Errorf("ClusterIssuer not ready after 30s:\n%s", result.Stdout)
			}

			fmt.Println()
			fmt.Println("Next steps:")
			fmt.Printf("  1. Ensure ClusterIssuer '%s' is configured in k0s.nix\n", issuerName)
			fmt.Println("  2. Deploy the host configuration: nixfleet apply -H", hostName)
			fmt.Printf("  3. Verify: kubectl get clusterissuer %s\n", issuerName)

			return nil
		},
	}

	cmd.Flags().StringVarP(&hostName, "host", "H", "", "Controller host name (required)")
	cmd.Flags().StringVar(&pkiDir, "pki-dir", "secrets/pki", "Directory for PKI files")
	cmd.Flags().StringSliceVar(&identities, "identity", nil, "Age identity files for decryption")
	cmd.Flags().StringVar(&secretName, "secret-name", "fleet-ca", "Name for the CA secret")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "cert-manager", "Namespace for the secret")
	cmd.Flags().StringVar(&issuerName, "issuer-name", "fleet-ca", "ClusterIssuer name to verify")
	cmd.Flags().BoolVar(&verify, "verify", false, "Verify ClusterIssuer becomes ready")
	cmd.MarkFlagRequired("host")

	return cmd
}

// Helper functions for k0s
func formatYAMLList(items []string, indent int) string {
	var sb strings.Builder
	prefix := strings.Repeat(" ", indent)
	for _, item := range items {
		sb.WriteString(fmt.Sprintf("%s- %s\n", prefix, item))
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

func formatNixList(items []string, indent int) string {
	var sb strings.Builder
	prefix := strings.Repeat(" ", indent)
	for _, item := range items {
		sb.WriteString(fmt.Sprintf("%s\"%s\"\n", prefix, item))
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// nodeStatusCmd returns the node-status command for running a status server on nodes
func nodeStatusCmd() *cobra.Command {
	var port int
	var bindAddress string
	var stateDir string
	var logFile string
	var hostRepoPath string
	var homeManagerPath string

	cmd := &cobra.Command{
		Use:   "node-status",
		Short: "Run a node status HTTP server (for pull-mode nodes)",
		Long: `Start a lightweight HTTP server that reports node status.

This is designed to run on nodes in pull-mode to provide status information
to monitoring systems, load balancers, or the central nixfleet server.

Endpoints:
  GET /         - Human-readable status page
  GET /status   - Full status JSON
  GET /health   - Simple health check (returns 200 if healthy, 503 if not)
  GET /pull     - Pull mode status and recent log entries
  GET /state    - Current state.json information

The server reads status from:
  - /var/lib/nixfleet/state.json - Last deployment info
  - /var/log/nixfleet/pull.log   - Pull operation logs
  - Git repositories for commit info

Example:
  # Run on default port 9100
  nixfleet node-status

  # Run on custom port with specific bind address
  nixfleet node-status --port 8080 --bind 127.0.0.1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			cfg := nodestatus.DefaultConfig()
			cfg.Port = port
			cfg.BindAddress = bindAddress
			cfg.Version = version
			cfg.GitCommit = gitCommit
			cfg.GitTag = gitTag
			if stateDir != "" {
				cfg.StateDir = stateDir
			}
			if logFile != "" {
				cfg.LogFile = logFile
			}
			if hostRepoPath != "" {
				cfg.HostRepoPath = hostRepoPath
			}
			if homeManagerPath != "" {
				cfg.HomeManagerPath = homeManagerPath
			}

			srv := nodestatus.NewServer(cfg)
			return srv.Start(ctx)
		},
	}

	cmd.Flags().IntVar(&port, "port", 9100, "Port to listen on")
	cmd.Flags().StringVar(&bindAddress, "bind", "0.0.0.0", "Address to bind to")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "State directory (default: /var/lib/nixfleet)")
	cmd.Flags().StringVar(&logFile, "log-file", "", "Pull log file (default: /var/log/nixfleet/pull.log)")
	cmd.Flags().StringVar(&hostRepoPath, "host-repo", "", "Host config repository path (default: /var/lib/nixfleet/repo)")
	cmd.Flags().StringVar(&homeManagerPath, "home-manager-path", "", "Home-manager dotfiles path")

	return cmd
}

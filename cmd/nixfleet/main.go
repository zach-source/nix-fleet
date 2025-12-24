package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nixfleet/nixfleet/internal/cache"
	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/osupdate"
	"github.com/nixfleet/nixfleet/internal/pullmode"
	"github.com/nixfleet/nixfleet/internal/reboot"
	"github.com/nixfleet/nixfleet/internal/secrets"
	"github.com/nixfleet/nixfleet/internal/server"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
	"github.com/spf13/cobra"
)

var version = "dev"

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
	var skipPreflight, skipHealth, skipState bool

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

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install pull mode on hosts",
		Long: `Install and configure pull mode on target hosts.

This will:
  1. Set up SSH config for Git repository access
  2. Clone the configuration repository
  3. Install the nixfleet-pull script
  4. Create and enable systemd timer for periodic pulls

Example:
  nixfleet pull-mode install -H gtr --repo git@github.com:org/fleet-config.git`,
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

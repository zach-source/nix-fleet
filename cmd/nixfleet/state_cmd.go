package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
	"github.com/spf13/cobra"
)

// stateCmd groups commands for inspecting and seeding per-host deployment state.
func stateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Inspect and manage NixFleet host state",
		Long:  `Manage the per-host deployment state recorded at /var/lib/nixfleet/state.json.`,
	}
	cmd.AddCommand(stateAdoptCmd())
	return cmd
}

// stateAdoptCmd imports out-of-band hosts into NixFleet state so that `plan`/`apply`
// no longer treat them as a NEW DEPLOYMENT. It computes the expected on-host file
// state from the declared config, reports drift, and (unless --dry-run) records the
// host as deployed at the current manifest hash.
func stateAdoptCmd() *cobra.Command {
	var (
		dryRun bool
		force  bool
	)

	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Import existing hosts into NixFleet state (clears NEW DEPLOYMENT)",
		Long: `Adopt one or more out-of-band hosts into NixFleet's state tracking.

For each target host it:
  1. Builds the host's declared configuration (manifest hash + store path).
  2. Computes the expected on-host file state from config.nixfleet.files.
  3. Reads the host and reports drift (managed files that differ or are missing).
  4. Writes /var/lib/nixfleet/state.json recording the host as deployed at that
     manifest hash (unless --dry-run).

By default adopt refuses when drift is detected (the host does not match the
declared config) so it never records a false baseline; use --force to record
state anyway. Run with --dry-run first to see the drift report.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			_, hosts, err := loadInventoryAndHosts(ctx)
			if err != nil {
				return err
			}

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

			adopted, skipped := 0, 0
			for _, host := range hosts {
				fmt.Printf("Host: %s (%s @ %s)\n", host.Name, host.Base, host.Addr)

				if host.Base != "ubuntu" {
					fmt.Printf("  skip: adopt only supports ubuntu hosts (base=%s)\n\n", host.Base)
					skipped++
					continue
				}

				// 1. Build the declared config -> manifest hash + store path.
				closure, err := evaluator.BuildHost(ctx, host.Name, host.Base)
				if err != nil {
					fmt.Printf("  ERROR building config: %v\n\n", err)
					skipped++
					continue
				}

				// 2. Expected managed-file state from the declared config.
				declared, err := evaluator.EvalManagedFiles(ctx, host.Name)
				if err != nil {
					fmt.Printf("  ERROR evaluating files: %v\n\n", err)
					skipped++
					continue
				}
				expected, err := expectedManagedFiles(declared)
				if err != nil {
					fmt.Printf("  ERROR computing expected file state: %v\n\n", err)
					skipped++
					continue
				}

				// 3. Connect, read current state, report drift.
				client, err := pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
				if err != nil {
					fmt.Printf("  ERROR connecting: %v\n\n", err)
					skipped++
					continue
				}

				current, _ := stateMgr.ReadState(ctx, client)
				if current == nil {
					current = state.NewHostState(host.Name, host.Base)
				}

				status := "NEW DEPLOYMENT"
				switch {
				case current.ManifestHash == "":
				case current.ManifestHash == closure.ManifestHash:
					status = "already recorded (matching hash)"
				default:
					status = "recorded at a different hash"
				}
				fmt.Printf("  Current state: %s\n", status)
				fmt.Printf("  Manifest hash to record: %s\n", closure.ManifestHash)

				results, err := stateMgr.CheckDrift(ctx, client, expected)
				if err != nil {
					fmt.Printf("  ERROR checking drift: %v\n\n", err)
					skipped++
					continue
				}
				driftCount := 0
				for _, r := range results {
					if r.HasDrift() {
						driftCount++
						fmt.Printf("    drift: %s (%s)\n", r.Path, r.Status)
					}
				}
				fmt.Printf("  Managed files: %d declared, %d drifted\n", len(expected), driftCount)

				if dryRun {
					fmt.Printf("  (dry-run) would record state at generation %d\n\n", current.CurrentGeneration+1)
					continue
				}
				if driftCount > 0 && !force {
					fmt.Printf("  REFUSED: host differs from declared config; resolve drift or re-run with --force\n\n")
					skipped++
					continue
				}

				// 4. Record adopted state.
				current.Hostname = host.Name
				current.Base = host.Base
				current.StorePath = closure.StorePath
				current.ManifestHash = closure.ManifestHash
				current.ManagedFiles = expected
				current.LastApply = time.Now()
				if current.CurrentGeneration == 0 {
					current.CurrentGeneration = 1
				}
				if err := stateMgr.WriteState(ctx, client, current); err != nil {
					fmt.Printf("  ERROR writing state: %v\n\n", err)
					skipped++
					continue
				}
				fmt.Printf("  ADOPTED at generation %d\n\n", current.CurrentGeneration)
				adopted++
			}

			fmt.Printf("Adopted %d host(s), skipped %d\n", adopted, skipped)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report drift and intended state without writing")
	cmd.Flags().BoolVar(&force, "force", false, "Adopt even when the host has drifted from the declared config")
	return cmd
}

// expectedManagedFiles converts declared files into the FileState map used for
// drift detection: sha256 of the file content (matching `sha256sum` on the host)
// plus the declared mode/owner/group. Files with neither text nor source are
// skipped (nothing to hash).
func expectedManagedFiles(declared map[string]nix.DeclaredFile) (map[string]state.FileState, error) {
	out := make(map[string]state.FileState, len(declared))
	for path, df := range declared {
		var content []byte
		switch {
		case df.Text != nil:
			content = []byte(*df.Text)
		case df.Source != nil:
			b, err := os.ReadFile(*df.Source)
			if err != nil {
				return nil, fmt.Errorf("reading source for %s: %w", path, err)
			}
			content = b
		default:
			continue
		}
		sum := sha256.Sum256(content)
		out[path] = state.FileState{
			Path:         path,
			Hash:         hex.EncodeToString(sum[:]),
			Mode:         df.Mode,
			Owner:        df.Owner,
			Group:        df.Group,
			RestartUnits: df.RestartUnits,
		}
	}
	return out, nil
}

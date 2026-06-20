package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/synology"
	"github.com/spf13/cobra"
)

func synologyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "synology",
		Short: "Manage a Synology NAS via the DSM API (Model B)",
		Long: `Declaratively manage a Synology NAS over the DSM Web API.

Desired state is declared in Nix under 'nixfleet.synology' (iSCSI LUNs and NFS
exports in this slice) and reconciled here — no SSH/closure involved. The DSM
password is sourced out-of-band (never from Nix):

  --password-command "op read 'op://Personal Agents/nas botuser/confirmpassword'"
  or  $SYNOLOGY_PASSWORD

Examples:
  nixfleet synology status znas
  nixfleet synology reconcile znas               # dry-run (default)
  nixfleet synology reconcile znas --apply       # create missing LUNs + set NFS
  nixfleet synology reconcile znas --apply --allow-resize --prune`,
	}
	cmd.PersistentFlags().String("password-command", "",
		"Command whose stdout is the DSM password (falls back to $SYNOLOGY_PASSWORD)")
	cmd.AddCommand(synologyStatusCmd())
	cmd.AddCommand(synologyReconcileCmd())
	cmd.AddCommand(synologyGetCmd())
	return cmd
}

// synologyGetCmd is a generic DSM API reader — useful for discovering the shape
// of any setting before declaring it under nixfleet.synology.settings.
func synologyGetCmd() *cobra.Command {
	var version int
	var params []string
	cmd := &cobra.Command{
		Use:   "get <host> <api> [method]",
		Short: "Read any DSM API (e.g. SYNO.Core.FileServ.NFS get)",
		Long: `Generic DSM API read. Examples:
  nixfleet synology get znas SYNO.Core.FileServ.NFS get
  nixfleet synology get znas SYNO.Core.Network get --version 2
  nixfleet synology get znas SYNO.Core.Share list --param 'additional=["vol_path"]'`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			host, api := args[0], args[1]
			method := "get"
			if len(args) == 3 {
				method = args[2]
			}
			pm := map[string]string{}
			for _, kv := range params {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("bad --param %q (want key=value)", kv)
				}
				pm[k] = v
			}
			cl, _, err := synologyConnect(ctx, cmd, host)
			if err != nil {
				return err
			}
			defer cl.Logout(ctx)
			raw, err := cl.Call(ctx, api, method, version, pm)
			if err != nil {
				return err
			}
			var pretty any
			if json.Unmarshal(raw, &pretty) == nil {
				b, _ := json.MarshalIndent(pretty, "", "  ")
				fmt.Println(string(b))
			} else {
				fmt.Println(string(raw))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&version, "version", 1, "API version")
	cmd.Flags().StringArrayVar(&params, "param", nil, "Query param key=value (repeatable)")
	return cmd
}

// synologyHostArg resolves the host name (a nixfleetConfigurations key) from a
// positional arg or the global -H flag.
func synologyHostArg(args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if targetHost != "" {
		return targetHost, nil
	}
	return "", fmt.Errorf("specify a host: nixfleet synology <status|reconcile> <host> (or -H)")
}

// synologyLoadConfig evaluates the declarative DSM spec for a host.
func synologyLoadConfig(ctx context.Context, host string) (*synology.Config, error) {
	flake, err := nix.ResolveFlakePath(flakePath)
	if err != nil {
		return nil, err
	}
	ev, err := nix.NewEvaluator(flake)
	if err != nil {
		return nil, err
	}
	raw, err := ev.EvalAttrJSON(ctx, fmt.Sprintf("nixfleetConfigurations.%s.synology", host))
	if err != nil {
		return nil, err
	}
	var cfg synology.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode synology config for %s: %w", host, err)
	}
	if !cfg.Enable {
		return nil, fmt.Errorf("host %q does not set nixfleet.synology.enable = true", host)
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("host %q: nixfleet.synology.host is empty", host)
	}
	return &cfg, nil
}

// synologyPassword resolves the DSM password without ever logging it.
func synologyPassword(cmd *cobra.Command) (string, error) {
	if pc, _ := cmd.Flags().GetString("password-command"); pc != "" {
		out, err := exec.Command("sh", "-c", pc).Output()
		if err != nil {
			return "", fmt.Errorf("password-command failed: %w", err)
		}
		pw := strings.TrimSpace(string(out))
		if pw == "" {
			return "", fmt.Errorf("password-command produced no output")
		}
		return pw, nil
	}
	if p := os.Getenv("SYNOLOGY_PASSWORD"); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no DSM password: pass --password-command or set $SYNOLOGY_PASSWORD")
}

// synologyConnect evaluates config, resolves the password, and logs in.
func synologyConnect(ctx context.Context, cmd *cobra.Command, host string) (*synology.Client, *synology.Config, error) {
	cfg, err := synologyLoadConfig(ctx, host)
	if err != nil {
		return nil, nil, err
	}
	pw, err := synologyPassword(cmd)
	if err != nil {
		return nil, nil, err
	}
	cl := synology.NewClient(cfg.Host, cfg.Port, cfg.HTTPS, cfg.User, pw)
	if err := cl.Login(ctx); err != nil {
		return nil, nil, err
	}
	return cl, cfg, nil
}

func synologyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [host]",
		Short: "Show live DSM state (iSCSI LUNs + NFS shares)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			host, err := synologyHostArg(args)
			if err != nil {
				return err
			}
			cl, cfg, err := synologyConnect(ctx, cmd, host)
			if err != nil {
				return err
			}
			defer cl.Logout(ctx)

			fmt.Printf("Synology %q  (%s@%s:%d)\n\n", host, cfg.User, cfg.Host, cfg.Port)

			luns, err := cl.ListLUNs(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("iSCSI LUNs (%d):\n", len(luns))
			for _, l := range luns {
				fmt.Printf("  %-28s %-8s %-9s %s\n", l.Name, l.Type, synology.HumanBytes(l.Size), l.Location)
			}

			shares, err := cl.ListShares(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("\nShares (%d):\n", len(shares))
			for _, s := range shares {
				nfs := "—"
				if s.HasNFS() {
					nfs = "NFS"
				}
				fmt.Printf("  %-28s %-12s %s\n", s.Name, s.VolPath, nfs)
			}
			return nil
		},
	}
}

func synologyReconcileCmd() *cobra.Command {
	var apply, prune, allowResize bool
	cmd := &cobra.Command{
		Use:   "reconcile [host]",
		Short: "Diff declared (Nix) vs actual DSM state, optionally apply",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			host, err := synologyHostArg(args)
			if err != nil {
				return err
			}
			cl, cfg, err := synologyConnect(ctx, cmd, host)
			if err != nil {
				return err
			}
			defer cl.Logout(ctx)

			plan, err := synology.ComputePlan(ctx, cl, cfg)
			if err != nil {
				return err
			}
			fmt.Printf("Reconcile plan for %q:\n%s", host, plan.Render())

			nothingToApply := plan.Empty()
			if !apply {
				if !nothingToApply {
					fmt.Println("\n(dry-run — re-run with --apply to make changes)")
				}
				return nil
			}
			if nothingToApply && !prune {
				return nil
			}

			res := plan.Apply(ctx, cl, synology.ApplyOpts{Prune: prune, AllowResize: allowResize})
			for _, n := range res.Created {
				fmt.Printf("  created LUN %s\n", n)
			}
			for _, n := range res.Grown {
				fmt.Printf("  grew LUN %s\n", n)
			}
			for _, n := range res.NFSSet {
				fmt.Printf("  set NFS rules on %s\n", n)
			}
			for _, n := range res.Deleted {
				fmt.Printf("  deleted LUN %s\n", n)
			}
			for _, e := range res.Errors {
				fmt.Fprintf(os.Stderr, "  ERROR: %s\n", e)
			}
			if len(res.Errors) > 0 {
				return fmt.Errorf("%d error(s) during reconcile", len(res.Errors))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Make changes (default: dry-run)")
	cmd.Flags().BoolVar(&prune, "prune", false, "Delete LUNs that exist but aren't declared (DESTRUCTIVE)")
	cmd.Flags().BoolVar(&allowResize, "allow-resize", false, "Online-grow LUNs declared larger than actual")
	return cmd
}

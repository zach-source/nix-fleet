package synology

import (
	"context"
	"fmt"
	"strings"
)

// Plan is the diff between declared (Nix) and actual (DSM) state.
type Plan struct {
	LUNCreate  []LUN       // declared, missing on the NAS
	LUNGrow    []LUNResize // declared larger than actual (online grow)
	LUNShrink  []LUNResize // declared smaller — refused (reported only)
	LUNExtra   []ActualLUN // on the NAS, not declared (prune candidates)
	NFSSet     []NFSExport // declared exports whose share exists → rules will be set
	NFSNoShare []NFSExport // declared export but no matching share (manual create needed)
	Settings   []Setting   // generic DSM settings to apply (idempotent set calls)
}

// LUNResize captures a size delta for an existing LUN.
type LUNResize struct {
	Declared  LUN
	Actual    ActualLUN
	WantBytes int64
}

// Empty reports whether the plan would change anything.
func (p Plan) Empty() bool {
	return len(p.LUNCreate) == 0 && len(p.LUNGrow) == 0 && len(p.NFSSet) == 0 && len(p.Settings) == 0
}

// ComputePlan diffs the declared config against live DSM state.
func ComputePlan(ctx context.Context, c *Client, cfg *Config) (*Plan, error) {
	actualLUNs, err := c.ListLUNs(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]ActualLUN, len(actualLUNs))
	declared := make(map[string]bool, len(cfg.ISCSILUNs))
	for _, a := range actualLUNs {
		byName[a.Name] = a
	}

	p := &Plan{}
	for _, l := range cfg.ISCSILUNs {
		declared[l.Name] = true
		want, err := l.SizeBytes()
		if err != nil {
			return nil, fmt.Errorf("LUN %q: %w", l.Name, err)
		}
		a, ok := byName[l.Name]
		if !ok {
			p.LUNCreate = append(p.LUNCreate, l)
			continue
		}
		switch {
		case want > a.Size:
			p.LUNGrow = append(p.LUNGrow, LUNResize{Declared: l, Actual: a, WantBytes: want})
		case want < a.Size:
			p.LUNShrink = append(p.LUNShrink, LUNResize{Declared: l, Actual: a, WantBytes: want})
		}
	}
	for _, a := range actualLUNs {
		if !declared[a.Name] {
			p.LUNExtra = append(p.LUNExtra, a)
		}
	}

	// NFS exports: a declared export needs an existing share to attach rules to.
	shares, err := c.ListShares(ctx)
	if err != nil {
		return nil, err
	}
	shareSet := make(map[string]bool, len(shares))
	for _, s := range shares {
		shareSet[s.Name] = true
	}
	for _, e := range cfg.NFSExports {
		if !shareSet[e.Name] {
			p.NFSNoShare = append(p.NFSNoShare, e)
			continue
		}
		actual, err := c.LoadNFSRules(ctx, e.Name)
		if err != nil {
			return nil, err
		}
		if !NFSRulesMatch(e.Rules, actual) {
			p.NFSSet = append(p.NFSSet, e) // drift → rules will be (re)written
		}
	}

	// Generic settings are applied as declared (DSM set calls are idempotent;
	// we don't diff them since each api's get-shape differs).
	p.Settings = cfg.Settings
	return p, nil
}

// ApplyOpts gates the destructive parts of a reconcile.
type ApplyOpts struct {
	Prune       bool // delete LUNs that exist but aren't declared
	AllowResize bool // online-grow LUNs that are declared larger
}

// ApplyResult records what Apply did.
type ApplyResult struct {
	Created  []string
	Grown    []string
	Deleted  []string
	NFSSet   []string
	Settings []string
	Errors   []string
}

// Apply executes the plan. Caller is responsible for confirming intent (the CLI
// requires --apply). Create + NFS-set always run; delete/grow are opt-in.
func (p *Plan) Apply(ctx context.Context, c *Client, opts ApplyOpts) *ApplyResult {
	r := &ApplyResult{}
	for _, l := range p.LUNCreate {
		if _, err := c.CreateLUN(ctx, l); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("create LUN %s: %v", l.Name, err))
			continue
		}
		r.Created = append(r.Created, l.Name)
	}
	if opts.AllowResize {
		for _, g := range p.LUNGrow {
			if err := c.GrowLUN(ctx, g.Actual.UUID, g.WantBytes); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("grow LUN %s: %v", g.Declared.Name, err))
				continue
			}
			r.Grown = append(r.Grown, g.Declared.Name)
		}
	}
	for _, e := range p.NFSSet {
		rules := make([]DSMNFSRule, 0, len(e.Rules))
		for _, rule := range e.Rules {
			rules = append(rules, rule.toDSM())
		}
		if err := c.SaveNFSRules(ctx, e.Name, rules); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("set NFS %s: %v", e.Name, err))
			continue
		}
		r.NFSSet = append(r.NFSSet, e.Name)
	}
	for _, s := range p.Settings {
		if err := c.ApplySetting(ctx, s); err != nil {
			r.Errors = append(r.Errors, err.Error())
			continue
		}
		r.Settings = append(r.Settings, s.String())
	}
	if opts.Prune {
		for _, a := range p.LUNExtra {
			if err := c.DeleteLUN(ctx, a.UUID); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("delete LUN %s: %v", a.Name, err))
				continue
			}
			r.Deleted = append(r.Deleted, a.Name)
		}
	}
	return r
}

// Render returns a human-readable summary of the plan.
func (p Plan) Render() string {
	var b strings.Builder
	line := func(sym, msg string) { fmt.Fprintf(&b, "  %s %s\n", sym, msg) }
	for _, l := range p.LUNCreate {
		line("+", fmt.Sprintf("create LUN %q (%s, %s on %s)", l.Name, l.Size, l.dsmType(), l.Location))
	}
	for _, g := range p.LUNGrow {
		line("~", fmt.Sprintf("grow LUN %q %s → %s (needs --allow-resize)", g.Declared.Name, HumanBytes(g.Actual.Size), g.Declared.Size))
	}
	for _, s := range p.LUNShrink {
		line("!", fmt.Sprintf("LUN %q declared smaller (%s < %s) — REFUSED (no shrink)", s.Declared.Name, s.Declared.Size, HumanBytes(s.Actual.Size)))
	}
	for _, a := range p.LUNExtra {
		line("-", fmt.Sprintf("LUN %q exists but is not declared (delete only with --prune)", a.Name))
	}
	for _, e := range p.NFSSet {
		line("+", fmt.Sprintf("set %d NFS rule(s) on share %q", len(e.Rules), e.Name))
	}
	for _, e := range p.NFSNoShare {
		line("!", fmt.Sprintf("NFS export %q has no matching share — create the shared folder first", e.Name))
	}
	for _, s := range p.Settings {
		line("+", fmt.Sprintf("apply setting %s", s))
	}
	if b.Len() == 0 {
		return "  (no changes — NAS matches declared state)\n"
	}
	return b.String()
}

func HumanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for v := n / u; v >= u; v /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.0f%c", float64(n)/float64(div), "KMGT"[exp])
}

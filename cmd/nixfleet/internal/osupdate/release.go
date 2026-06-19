package osupdate

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// ReleaseInfo describes a host's distro release and the available upgrade.
type ReleaseInfo struct {
	CurrentVersion string // e.g. "25.10"
	Codename       string // e.g. "questing"
	TargetRelease  string // e.g. "26.04 LTS", "" if none offered
	// Supported means do-release-upgrade offers an upgrade target. This can be
	// true even when the *running* release is past EOL: Ubuntu still advertises
	// the next interim release (e.g. EOL 25.04 → 25.10), and do-release-upgrade
	// handles that hop normally. Only a stranded host (RunningEOL && no target)
	// needs the apt-sources codename-rewrite fallback.
	Supported bool
	// RunningEOL is true when the currently-installed release is itself past EOL
	// ("not supported anymore"). Informational; does not by itself block the
	// supported do-release-upgrade path.
	RunningEOL     bool
	ToolAvailable  bool  // do-release-upgrade present
	FreeRootMB     int64 // free space on / in MiB
	RebootRequired bool
}

// ReleaseUpgradeConfig controls a single host's release upgrade.
type ReleaseUpgradeConfig struct {
	// AllowEOL permits upgrading a host whose current release is EOL. Such hosts
	// cannot use do-release-upgrade; NextCodename must also be set so we can do a
	// sources-list codename rewrite + full-upgrade instead.
	AllowEOL bool
	// NextCodename is the codename of the next release to step an EOL host to
	// (e.g. "questing"). Only used on the EOL path. We never guess it.
	NextCodename string
	// PreHook runs (sudo) before the upgrade — e.g. cordon/drain k0s, stop
	// inference units. A non-zero exit aborts the upgrade.
	PreHook string
	// PostHook runs (sudo) after the host is verified back up — e.g. uncordon.
	PostHook string
	// MinFreeRootMB aborts the upgrade if / has less free space than this.
	MinFreeRootMB int64
	// LogPath is the host-side log for the detached upgrade.
	LogPath string
	// Unit is the transient systemd unit name the upgrade runs under.
	Unit string
}

// DefaultReleaseUpgradeConfig returns sane defaults.
func DefaultReleaseUpgradeConfig() ReleaseUpgradeConfig {
	return ReleaseUpgradeConfig{
		MinFreeRootMB: 8192, // ~8 GiB headroom for downloaded+unpacked packages
		LogPath:       "/var/log/nixfleet/release-upgrade.log",
		Unit:          "nixfleet-release-upgrade",
	}
}

// CheckReleaseInfo gathers the host's release + upgrade availability. Read-only.
func (u *Updater) CheckReleaseInfo(ctx context.Context, client *ssh.Client) (*ReleaseInfo, error) {
	info := &ReleaseInfo{}

	osRel, err := client.Exec(ctx, ". /etc/os-release && echo \"$VERSION_ID|$VERSION_CODENAME\"")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/os-release: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(osRel.Stdout), "|", 2)
	if len(parts) == 2 {
		info.CurrentVersion = parts[0]
		info.Codename = parts[1]
	}

	// do-release-upgrade -c: prints "New release 'X' available" or, for EOL,
	// "Your Ubuntu release is not supported anymore".
	if tool, _ := client.Exec(ctx, "command -v do-release-upgrade >/dev/null 2>&1 && echo yes || echo no"); tool != nil {
		info.ToolAvailable = strings.TrimSpace(tool.Stdout) == "yes"
	}
	if info.ToolAvailable {
		check, _ := client.Exec(ctx, "do-release-upgrade -c 2>&1 || true")
		out := check.Stdout
		// A target may be advertised even alongside an EOL notice, so detect both
		// independently. Supported == "a target is offered" (do-release-upgrade can
		// proceed); RunningEOL is purely informational.
		if m := regexp.MustCompile(`New release '([^']+)' available`).FindStringSubmatch(out); m != nil {
			info.TargetRelease = m[1]
		}
		info.RunningEOL = strings.Contains(out, "not supported anymore")
		info.Supported = info.TargetRelease != ""
	}

	if df, err := client.Exec(ctx, "df -Pm / | awk 'NR==2{print $4}'"); err == nil {
		info.FreeRootMB, _ = strconv.ParseInt(strings.TrimSpace(df.Stdout), 10, 64)
	}
	info.RebootRequired, _ = u.IsRebootRequired(ctx, client)

	return info, nil
}

// PrepareRelease readies a host for upgrade: sets the upgrade prompt to normal
// and fully patches the current release first (a clean within-release state is a
// precondition for a reliable release upgrade).
func (u *Updater) PrepareRelease(ctx context.Context, client *ssh.Client) error {
	// Prompt=normal so do-release-upgrade offers the next interim/LTS.
	if _, err := client.ExecSudo(ctx, "sed -i 's/^Prompt=.*/Prompt=normal/' /etc/update-manager/release-upgrades"); err != nil {
		return fmt.Errorf("setting release-upgrade prompt: %w", err)
	}
	if err := u.RefreshPackageCache(ctx, client); err != nil {
		return err
	}
	cmd := "DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::='--force-confdef' -o Dpkg::Options::='--force-confold' full-upgrade"
	res, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return fmt.Errorf("pre-upgrade full-upgrade: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("pre-upgrade full-upgrade failed: %s", res.Stderr)
	}
	return nil
}

// StartReleaseUpgrade launches the release upgrade detached under a transient
// systemd unit so it survives SSH disconnects. Returns once the unit is started.
// Use ReleaseUpgradeStatus to poll for completion.
//
// Supported releases use do-release-upgrade in its non-interactive server
// frontend. EOL releases (which do-release-upgrade refuses) instead get a
// codename rewrite of the apt sources + full-upgrade, gated behind AllowEOL +
// NextCodename so it is always an explicit operator choice.
func (u *Updater) StartReleaseUpgrade(ctx context.Context, client *ssh.Client, info *ReleaseInfo, cfg ReleaseUpgradeConfig) error {
	if cfg.MinFreeRootMB > 0 && info.FreeRootMB > 0 && info.FreeRootMB < cfg.MinFreeRootMB {
		return fmt.Errorf("insufficient free space on /: %d MiB < required %d MiB", info.FreeRootMB, cfg.MinFreeRootMB)
	}

	if cfg.PreHook != "" {
		res, err := client.ExecSudo(ctx, cfg.PreHook)
		if err != nil {
			return fmt.Errorf("pre-upgrade hook failed: %w", err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("pre-upgrade hook failed (exit %d): %s", res.ExitCode, res.Stderr)
		}
	}

	var upgradeCmd string
	switch {
	case info.TargetRelease != "":
		// do-release-upgrade advertises a target — use it even from an EOL release.
		upgradeCmd = "DEBIAN_FRONTEND=noninteractive do-release-upgrade -f DistUpgradeViewNonInteractive -m server"
	case cfg.AllowEOL && info.RunningEOL:
		// Stranded EOL host: no target offered. Fall back to a sources codename
		// rewrite + full-upgrade.
		if cfg.NextCodename == "" {
			return fmt.Errorf("host is EOL and requires --next-codename to rewrite apt sources (do-release-upgrade cannot upgrade an EOL release)")
		}
		if info.Codename == "" {
			return fmt.Errorf("could not determine current codename for EOL sources rewrite")
		}
		// Rewrite every occurrence of the current codename to the next across the
		// apt source definitions (handles both classic .list and deb822 .sources),
		// then do a full-upgrade onto the new release.
		from, to := info.Codename, cfg.NextCodename
		upgradeCmd = fmt.Sprintf(
			"set -e; "+
				"find /etc/apt/sources.list /etc/apt/sources.list.d/ -type f \\( -name '*.list' -o -name '*.sources' \\) "+
				"-exec sed -i 's/%s/%s/g' {} + ; "+
				"DEBIAN_FRONTEND=noninteractive apt-get update; "+
				"DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::='--force-confdef' -o Dpkg::Options::='--force-confold' full-upgrade",
			from, to)
	case info.RunningEOL:
		return fmt.Errorf("host release %q is EOL with no upgrade target offered; pass --allow-eol with --next-codename to rewrite apt sources", info.CurrentVersion)
	default:
		return fmt.Errorf("no upgrade target available for host release %q", info.CurrentVersion)
	}

	// Detach via systemd-run so the long upgrade outlives our SSH session, with
	// all output captured to LogPath for polling/forensics.
	logDir := cfg.LogPath[:strings.LastIndex(cfg.LogPath, "/")]
	launch := fmt.Sprintf(
		"mkdir -p %s && systemd-run --unit=%s --collect --setenv=DEBIAN_FRONTEND=noninteractive "+
			"/bin/bash -c %s",
		shQuote(logDir), shQuote(cfg.Unit),
		shQuote(fmt.Sprintf("{ %s ; } > %s 2>&1", upgradeCmd, cfg.LogPath)),
	)
	res, err := client.ExecSudo(ctx, launch)
	if err != nil {
		return fmt.Errorf("launching detached upgrade: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("launching detached upgrade failed: %s", res.Stderr)
	}
	return nil
}

// ReleaseUpgradeStatus polls the detached upgrade. running is true while the
// transient unit is active; when it stops, exitCode is its main process exit
// status (0 = success). tail is the last few log lines for progress display.
func (u *Updater) ReleaseUpgradeStatus(ctx context.Context, client *ssh.Client, cfg ReleaseUpgradeConfig) (running bool, exitCode int, tail string, err error) {
	act, err := client.Exec(ctx, fmt.Sprintf("systemctl is-active %s 2>/dev/null || true", shQuote(cfg.Unit)))
	if err != nil {
		return false, -1, "", err
	}
	state := strings.TrimSpace(act.Stdout)
	running = state == "active" || state == "activating" || state == "deactivating"

	if t, e := client.Exec(ctx, fmt.Sprintf("tail -n 5 %s 2>/dev/null || true", shQuote(cfg.LogPath))); e == nil {
		tail = strings.TrimRight(t.Stdout, "\n")
	}

	exitCode = -1
	if !running {
		if s, e := client.Exec(ctx, fmt.Sprintf("systemctl show %s -p ExecMainStatus --value 2>/dev/null || true", shQuote(cfg.Unit))); e == nil {
			if v, perr := strconv.Atoi(strings.TrimSpace(s.Stdout)); perr == nil {
				exitCode = v
			}
		}
	}
	return running, exitCode, tail, nil
}

// WaitForReleaseUpgrade blocks until the detached upgrade finishes or ctx/deadline
// expires, invoking onProgress with each polled log tail.
func (u *Updater) WaitForReleaseUpgrade(ctx context.Context, client *ssh.Client, cfg ReleaseUpgradeConfig, poll time.Duration, onProgress func(tail string)) (int, error) {
	if poll <= 0 {
		poll = 30 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		default:
		}
		running, exit, tail, err := u.ReleaseUpgradeStatus(ctx, client, cfg)
		if err != nil {
			return -1, err
		}
		if onProgress != nil && tail != "" {
			onProgress(tail)
		}
		if !running {
			return exit, nil
		}
		time.Sleep(poll)
	}
}

// VerifyRelease confirms the running release now matches wantVersion (a prefix
// match, e.g. "26.04"). Returns the observed VERSION_ID.
func (u *Updater) VerifyRelease(ctx context.Context, client *ssh.Client, wantVersion string) (bool, string, error) {
	res, err := client.Exec(ctx, ". /etc/os-release && echo \"$VERSION_ID\"")
	if err != nil {
		return false, "", err
	}
	got := strings.TrimSpace(res.Stdout)
	return strings.HasPrefix(got, wantVersion), got, nil
}

// shQuote single-quotes a string for safe embedding in a /bin/sh command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

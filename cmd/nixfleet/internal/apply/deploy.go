package apply

import (
	"context"
	"fmt"
	"time"

	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
)

// Phase names a step of a single-host deploy. Run reports the phase that
// failed so each caller can keep its own wording, and calls Progress as each
// phase starts so the CLI can keep printing what it used to print.
type Phase string

const (
	PhaseBuild    Phase = "build"
	PhaseCopy     Phase = "copy"
	PhaseConnect  Phase = "connect"
	PhaseActivate Phase = "activate"
	PhaseState    Phase = "state"
)

// PhaseError reports which step of a deploy failed.
type PhaseError struct {
	Phase Phase
	Err   error
}

func (e *PhaseError) Error() string { return fmt.Sprintf("%s failed: %v", e.Phase, e.Err) }
func (e *PhaseError) Unwrap() error { return e.Err }

// Builder builds a host's closure. *nix.Evaluator satisfies it.
type Builder interface {
	BuildHost(ctx context.Context, hostname, base string) (*nix.HostClosure, error)
}

// HostDeployer copies and activates a closure. *nix.Deployer satisfies it.
type HostDeployer interface {
	CopyToHost(ctx context.Context, closure *nix.HostClosure, host *inventory.Host) error
	ActivateUbuntu(ctx context.Context, client *ssh.Client, closure *nix.HostClosure) error
	ActivateNixOS(ctx context.Context, client *ssh.Client, closure *nix.HostClosure, action string) error
	ActivateDarwin(ctx context.Context, client *ssh.Client, closure *nix.HostClosure, action string) error
	GetCurrentGeneration(ctx context.Context, client *ssh.Client, base string) (int, string, error)
}

// StateRecorder records a completed apply. *state.Manager satisfies it.
type StateRecorder interface {
	UpdateAfterApply(ctx context.Context, client state.Execer, storePath, manifestHash string, generation int, duration time.Duration) error
}

// ClientPool hands out SSH connections. *ssh.Pool satisfies it.
type ClientPool interface {
	GetWithUser(ctx context.Context, host string, port int, user string) (*ssh.Client, error)
}

// HostDeploy performs the build -> copy -> connect -> activate -> record
// sequence for one host. `nixfleet apply`, `nixfleet nix update`, the HTTP
// server's two apply jobs and this package's pipeline each carried their own
// copy of it; they differ only in how they report progress and what they do
// afterwards, which is what DeployOptions and the returned result cover.
type HostDeploy struct {
	Builder  Builder
	Deployer HostDeployer
	Pool     ClientPool
	State    StateRecorder
}

// DeployOptions tunes a single deploy.
type DeployOptions struct {
	// Action is the NixOS/Darwin activation action (switch, boot, test).
	// Defaults to "switch" and is ignored on Ubuntu.
	Action string

	// SkipState leaves /var/lib/nixfleet/state.json untouched.
	SkipState bool

	// Progress, if set, is called as each phase starts.
	Progress func(Phase)
}

// Run deploys one host, returning what it did. A non-nil error is always a
// *PhaseError. The result is also returned alongside an error when the deploy
// got far enough to have something to report.
func (h HostDeploy) Run(ctx context.Context, host *inventory.Host, opts DeployOptions) (*DeployResult, error) {
	start := time.Now()

	action := opts.Action
	if action == "" {
		action = "switch"
	}

	h.progress(opts, PhaseBuild)
	closure, err := h.Builder.BuildHost(ctx, host.Name, host.Base)
	if err != nil {
		return nil, &PhaseError{Phase: PhaseBuild, Err: err}
	}

	result := &DeployResult{
		Closure:      closure.StorePath,
		ManifestHash: closure.ManifestHash,
		Action:       action,
	}

	h.progress(opts, PhaseCopy)
	if err := h.Deployer.CopyToHost(ctx, closure, host); err != nil {
		return result, &PhaseError{Phase: PhaseCopy, Err: err}
	}

	h.progress(opts, PhaseConnect)
	port := host.SSHPort
	if port == 0 {
		port = 22
	}
	client, err := h.Pool.GetWithUser(ctx, host.Addr, port, host.SSHUser)
	if err != nil {
		return result, &PhaseError{Phase: PhaseConnect, Err: err}
	}
	result.Client = client

	h.progress(opts, PhaseActivate)
	if err := h.activate(ctx, client, closure, host.Base, action); err != nil {
		return result, &PhaseError{Phase: PhaseActivate, Err: err}
	}

	result.Duration = time.Since(start)

	if !opts.SkipState {
		h.progress(opts, PhaseState)
		gen, _, genErr := h.Deployer.GetCurrentGeneration(ctx, client, host.Base)
		result.Generation = gen
		// Recording state is not worth failing a deploy that already succeeded,
		// so report it and let the caller decide what to say.
		if genErr != nil {
			result.StateError = fmt.Sprintf("reading generation: %v", genErr)
		}
		if err := h.State.UpdateAfterApply(ctx, client, closure.StorePath, closure.ManifestHash, gen, result.Duration); err != nil {
			result.StateError = err.Error()
		} else if result.StateError == "" {
			result.StateUpdated = true
		}
	}

	return result, nil
}

// activate dispatches on the host's base. Every call site had its own copy of
// this switch and three of them listed only ubuntu and nixos, so a darwin host
// was reported as deployed without anything being activated, and an
// unrecognised base did the same.
func (h HostDeploy) activate(ctx context.Context, client *ssh.Client, closure *nix.HostClosure, base, action string) error {
	switch inventory.NormalizeBase(base) {
	case inventory.BaseUbuntu:
		return h.Deployer.ActivateUbuntu(ctx, client, closure)
	case inventory.BaseNixOS:
		return h.Deployer.ActivateNixOS(ctx, client, closure, action)
	case inventory.BaseDarwin:
		return h.Deployer.ActivateDarwin(ctx, client, closure, action)
	default:
		return fmt.Errorf("unknown host base: %s", base)
	}
}

func (h HostDeploy) progress(opts DeployOptions, p Phase) {
	if opts.Progress != nil {
		opts.Progress(p)
	}
}

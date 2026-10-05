package apply

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/nix"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
)

// fakeDeployer records the sequence it was driven through. It ignores the
// *ssh.Client, so the tests can run the whole deploy with a nil connection.
type fakeDeployer struct {
	calls     []string
	buildErr  error
	copyErr   error
	activErr  error
	gen       int
	genErr    error
	stateErr  error
	connErr   error
	stateArgs []any
}

func (f *fakeDeployer) BuildHost(ctx context.Context, hostname, base string) (*nix.HostClosure, error) {
	f.calls = append(f.calls, "build")
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	return &nix.HostClosure{StorePath: "/nix/store/abc-system", ManifestHash: "cafebabe", Base: base}, nil
}

func (f *fakeDeployer) CopyToHost(ctx context.Context, closure *nix.HostClosure, host *inventory.Host) error {
	f.calls = append(f.calls, "copy")
	return f.copyErr
}

func (f *fakeDeployer) ActivateUbuntu(ctx context.Context, client *ssh.Client, closure *nix.HostClosure) error {
	f.calls = append(f.calls, "activate-ubuntu")
	return f.activErr
}

func (f *fakeDeployer) ActivateNixOS(ctx context.Context, client *ssh.Client, closure *nix.HostClosure, action string) error {
	f.calls = append(f.calls, "activate-nixos:"+action)
	return f.activErr
}

func (f *fakeDeployer) ActivateDarwin(ctx context.Context, client *ssh.Client, closure *nix.HostClosure, action string) error {
	f.calls = append(f.calls, "activate-darwin:"+action)
	return f.activErr
}

func (f *fakeDeployer) GetCurrentGeneration(ctx context.Context, client *ssh.Client, base string) (int, string, error) {
	f.calls = append(f.calls, "generation")
	return f.gen, "", f.genErr
}

func (f *fakeDeployer) UpdateAfterApply(ctx context.Context, client state.Execer, storePath, manifestHash string, generation int, duration time.Duration) error {
	f.calls = append(f.calls, "record-state")
	f.stateArgs = []any{storePath, manifestHash, generation}
	return f.stateErr
}

func (f *fakeDeployer) GetWithUser(ctx context.Context, host string, port int, user string) (*ssh.Client, error) {
	f.calls = append(f.calls, "connect")
	if f.connErr != nil {
		return nil, f.connErr
	}
	return nil, nil
}

func deployFor(f *fakeDeployer) HostDeploy {
	return HostDeploy{Builder: f, Deployer: f, Pool: f, State: f}
}

func TestRunDeploysUbuntuAndRecordsState(t *testing.T) {
	f := &fakeDeployer{gen: 7}
	host := &inventory.Host{Name: "gtr-150", Base: "ubuntu", Addr: "10.0.0.1"}

	result, err := deployFor(f).Run(context.Background(), host, DeployOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"build", "copy", "connect", "activate-ubuntu", "generation", "record-state"}
	if got := f.calls; !equal(got, want) {
		t.Errorf("sequence = %v, want %v", got, want)
	}
	if result.Generation != 7 || !result.StateUpdated || result.StateError != "" {
		t.Errorf("state not recorded: %+v", result)
	}
	if result.Closure != "/nix/store/abc-system" || result.ManifestHash != "cafebabe" {
		t.Errorf("closure not reported: %+v", result)
	}
	if got := f.stateArgs; got[0] != "/nix/store/abc-system" || got[1] != "cafebabe" || got[2] != 7 {
		t.Errorf("state recorded with %v", got)
	}
}

// A DGX host is deployed exactly like Ubuntu (inventory.NormalizeBase), and a
// darwin host must actually be activated: three of the four call sites this
// replaces listed only ubuntu and nixos, so a darwin deploy reported success
// having activated nothing.
func TestRunActivatesEveryKnownBase(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"ubuntu", "activate-ubuntu"},
		{"dgx", "activate-ubuntu"},
		{"nixos", "activate-nixos:switch"},
		{"darwin", "activate-darwin:switch"},
	} {
		f := &fakeDeployer{}
		host := &inventory.Host{Name: "h", Base: tc.base}
		if _, err := deployFor(f).Run(context.Background(), host, DeployOptions{}); err != nil {
			t.Fatalf("base %s: %v", tc.base, err)
		}
		if !contains(f.calls, tc.want) {
			t.Errorf("base %s: expected %s, got %v", tc.base, tc.want, f.calls)
		}
	}
}

// An unrecognised base is an error, not a silent success.
func TestRunRejectsUnknownBase(t *testing.T) {
	f := &fakeDeployer{}
	host := &inventory.Host{Name: "h", Base: "plan9"}

	_, err := deployFor(f).Run(context.Background(), host, DeployOptions{})
	if err == nil {
		t.Fatal("expected an error for an unknown base")
	}
	var phaseErr *PhaseError
	if !errors.As(err, &phaseErr) || phaseErr.Phase != PhaseActivate {
		t.Errorf("expected an activate PhaseError, got %v", err)
	}
	if contains(f.calls, "record-state") {
		t.Error("state was recorded for a host that was never activated")
	}
}

func TestRunPassesTheActionThrough(t *testing.T) {
	f := &fakeDeployer{}
	host := &inventory.Host{Name: "h", Base: "nixos"}

	if _, err := deployFor(f).Run(context.Background(), host, DeployOptions{Action: "boot"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !contains(f.calls, "activate-nixos:boot") {
		t.Errorf("action not passed through: %v", f.calls)
	}
}

func TestRunReportsTheFailingPhase(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name  string
		setup func(*fakeDeployer)
		phase Phase
		after []string
	}{
		{"build", func(f *fakeDeployer) { f.buildErr = boom }, PhaseBuild, []string{"copy", "connect", "activate-ubuntu", "record-state"}},
		{"copy", func(f *fakeDeployer) { f.copyErr = boom }, PhaseCopy, []string{"connect", "activate-ubuntu", "record-state"}},
		{"connect", func(f *fakeDeployer) { f.connErr = boom }, PhaseConnect, []string{"activate-ubuntu", "record-state"}},
		{"activate", func(f *fakeDeployer) { f.activErr = boom }, PhaseActivate, []string{"record-state"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDeployer{}
			tc.setup(f)
			host := &inventory.Host{Name: "h", Base: "ubuntu"}

			_, err := deployFor(f).Run(context.Background(), host, DeployOptions{})
			var phaseErr *PhaseError
			if !errors.As(err, &phaseErr) {
				t.Fatalf("expected a *PhaseError, got %v", err)
			}
			if phaseErr.Phase != tc.phase {
				t.Errorf("phase = %q, want %q", phaseErr.Phase, tc.phase)
			}
			if !errors.Is(err, boom) {
				t.Errorf("error does not wrap the cause: %v", err)
			}
			for _, later := range tc.after {
				if contains(f.calls, later) {
					t.Errorf("continued to %q after %s failed: %v", later, tc.name, f.calls)
				}
			}
		})
	}
}

// Failing to record state must not fail a deploy that already succeeded.
func TestRunReportsStateFailureWithoutFailingTheDeploy(t *testing.T) {
	f := &fakeDeployer{stateErr: errors.New("permission denied")}
	host := &inventory.Host{Name: "h", Base: "ubuntu"}

	result, err := deployFor(f).Run(context.Background(), host, DeployOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.StateUpdated {
		t.Error("StateUpdated set despite the write failing")
	}
	if result.StateError != "permission denied" {
		t.Errorf("StateError = %q", result.StateError)
	}
}

func TestRunSkipState(t *testing.T) {
	f := &fakeDeployer{}
	host := &inventory.Host{Name: "h", Base: "ubuntu"}

	if _, err := deployFor(f).Run(context.Background(), host, DeployOptions{SkipState: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if contains(f.calls, "record-state") || contains(f.calls, "generation") {
		t.Errorf("state was touched despite SkipState: %v", f.calls)
	}
}

func TestRunReportsProgressPerPhase(t *testing.T) {
	f := &fakeDeployer{}
	host := &inventory.Host{Name: "h", Base: "ubuntu"}

	var seen []Phase
	if _, err := deployFor(f).Run(context.Background(), host, DeployOptions{
		Progress: func(p Phase, _ *DeployResult) { seen = append(seen, p) },
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []Phase{PhaseBuild, PhaseCopy, PhaseConnect, PhaseActivate, PhaseState}
	if len(seen) != len(want) {
		t.Fatalf("phases = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("phases = %v, want %v", seen, want)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

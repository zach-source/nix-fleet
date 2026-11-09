package apply

import (
	"context"
	"errors"
	"github.com/nixfleet/nixfleet/internal/inventory"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"strings"
	"testing"
)

type fakeChecks struct {
	calls                   *[]string
	preflightErr, healthErr error
}

func (f fakeChecks) Preflight(context.Context, *ssh.Client, *inventory.Host) error {
	*f.calls = append(*f.calls, "preflight")
	return f.preflightErr
}
func (f fakeChecks) Health(context.Context, *ssh.Client, string) error {
	*f.calls = append(*f.calls, "health")
	return f.healthErr
}
func TestVerifiedDeployGatesActivationAndReportsHealthFailure(t *testing.T) {
	for _, phase := range []string{"preflight", "health"} {
		f := &fakeDeployer{}
		checks := fakeChecks{calls: &f.calls}
		if phase == "preflight" {
			checks.preflightErr = errors.New("no disk")
		} else {
			checks.healthErr = errors.New("unhealthy")
		}
		deploy := deployFor(f)
		deploy.Checks = checks
		result, err := deploy.Run(context.Background(), &inventory.Host{Name: "fixture", Base: "ubuntu"}, DeployOptions{Verify: true})
		if err == nil || !strings.Contains(err.Error(), phase) {
			t.Fatalf("%s did not fail: %v", phase, err)
		}
		calls := strings.Join(f.calls, ",")
		if phase == "preflight" && strings.Contains(calls, "activate") {
			t.Fatal("activated after failed preflight")
		}
		if phase == "health" && (!result.StateUpdated || !strings.Contains(calls, "record-state,health")) {
			t.Fatalf("actual deployment state lost on health failure: %s", calls)
		}
	}
}
func TestLegacyDeployDoesNotRunOptInChecks(t *testing.T) {
	f := &fakeDeployer{}
	deploy := deployFor(f)
	deploy.Checks = fakeChecks{calls: &f.calls, preflightErr: errors.New("disabled")}
	if _, err := deploy.Run(context.Background(), &inventory.Host{Base: "ubuntu"}, DeployOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(f.calls, ","), "preflight") {
		t.Fatal("legacy behavior changed")
	}
}

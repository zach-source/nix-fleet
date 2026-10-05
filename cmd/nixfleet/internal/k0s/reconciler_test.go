package k0s

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
)

// fakeExec is a state.Execer that answers the first response whose match
// substring appears in the command, and records every command it was asked to
// run. Unmatched commands succeed with empty output.
type fakeExec struct {
	responses []fakeResponse
	calls     []string
}

type fakeResponse struct {
	match  string
	result *ssh.ExecResult
	err    error
}

func (f *fakeExec) Exec(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	f.calls = append(f.calls, cmd)
	for _, r := range f.responses {
		if strings.Contains(cmd, r.match) {
			return r.result, r.err
		}
	}
	return &ssh.ExecResult{}, nil
}

func (f *fakeExec) ExecSudo(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	return f.Exec(ctx, "sudo "+cmd)
}

func (f *fakeExec) ran(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func ok(stdout string) *ssh.ExecResult { return &ssh.ExecResult{Stdout: stdout} }

// A namespace must not be deleted when the pod listing failed. The old code
// piped through `wc -l`, so kubectl's failure became a count of "0" and the
// namespace was deleted along with whatever was still running in it.
func TestDeleteHelmChartKeepsNamespaceWhenPodListingFails(t *testing.T) {
	f := &fakeExec{responses: []fakeResponse{
		{match: "get chart k0s-addon-chart-attic", result: ok("attic")},
		{match: "get pods -n attic", result: &ssh.ExecResult{ExitCode: 1, Stderr: "connection refused"}},
	}}

	if err := NewReconciler().deleteHelmChart(context.Background(), f, "attic"); err != nil {
		t.Fatalf("deleteHelmChart: %v", err)
	}
	if f.ran("delete namespace attic") {
		t.Error("namespace was deleted despite the pod listing failing")
	}
	if f.ran("wc -l") {
		t.Error("pod count must not go through a pipeline, which masks kubectl's exit status")
	}
}

// Same, for a transport-level failure: Exec returns (nil, err), which used to
// panic on result.Stdout.
func TestDeleteHelmChartSurvivesExecError(t *testing.T) {
	f := &fakeExec{responses: []fakeResponse{
		{match: "get chart k0s-addon-chart-attic", result: ok("attic")},
		{match: "get pods -n attic", err: errors.New("ssh: connection lost")},
	}}

	if err := NewReconciler().deleteHelmChart(context.Background(), f, "attic"); err != nil {
		t.Fatalf("deleteHelmChart: %v", err)
	}
	if f.ran("delete namespace attic") {
		t.Error("namespace was deleted despite the pod listing erroring")
	}
}

// If the namespace lookup itself fails we know nothing about where the chart
// lives, so nothing may be deleted.
func TestDeleteHelmChartAbortsWhenNamespaceLookupFails(t *testing.T) {
	f := &fakeExec{responses: []fakeResponse{
		{match: "get chart k0s-addon-chart-attic", err: errors.New("ssh: connection lost")},
	}}

	err := NewReconciler().deleteHelmChart(context.Background(), f, "attic")
	if err == nil {
		t.Fatal("expected an error when the namespace lookup fails")
	}
	if f.ran("delete") {
		t.Errorf("nothing may be deleted with an unknown namespace, ran: %v", f.calls)
	}
}

// The happy path still deletes an empty namespace.
func TestDeleteHelmChartDeletesEmptyNamespace(t *testing.T) {
	f := &fakeExec{responses: []fakeResponse{
		{match: "get chart k0s-addon-chart-attic", result: ok("attic")},
		{match: "get pods -n attic", result: ok("\n")},
	}}

	if err := NewReconciler().deleteHelmChart(context.Background(), f, "attic"); err != nil {
		t.Fatalf("deleteHelmChart: %v", err)
	}
	if !f.ran("delete namespace attic") {
		t.Errorf("empty namespace was not deleted, ran: %v", f.calls)
	}
}

// A non-empty namespace is left alone.
func TestDeleteHelmChartKeepsNonEmptyNamespace(t *testing.T) {
	f := &fakeExec{responses: []fakeResponse{
		{match: "get chart k0s-addon-chart-attic", result: ok("attic")},
		{match: "get pods -n attic", result: ok("pod/atticd-0\n")},
	}}

	if err := NewReconciler().deleteHelmChart(context.Background(), f, "attic"); err != nil {
		t.Fatalf("deleteHelmChart: %v", err)
	}
	if f.ran("delete namespace attic") {
		t.Error("namespace with a running pod was deleted")
	}
}

// Orphan cleanup must delete the orphaned resource, not whatever sits at the
// same index in the previous state. The orphan is deliberately second here:
// the old code indexed previousState.Manifests by position in the filtered
// orphan list and would have deleted the still-configured ConfigMap instead.
func TestReconcileDeletesTheOrphanedResource(t *testing.T) {
	const keepYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: keep-me
  namespace: bar
`
	f := &fakeExec{responses: []fakeResponse{
		{match: "cat /etc/k0s/k0s.yaml", result: ok("apiVersion: k0s.k0sproject.io/v1beta1\n")},
		{match: "ls -1 /var/lib/k0s/manifests", result: ok("/var/lib/k0s/manifests/keep.yaml\n")},
		{match: "cat /var/lib/k0s/manifests/keep.yaml", result: ok(keepYAML)},
	}}

	previous := &state.K0sState{Manifests: []state.K0sManifestState{
		{Kind: "ConfigMap", Name: "keep-me", Namespace: "bar", ManifestFile: "keep.yaml"},
		{Kind: "CiliumLoadBalancerIPPool", Name: "lb-pool", ManifestFile: "cilium-lb-pool.yaml"},
	}}

	result, err := NewReconciler().Reconcile(context.Background(), f, previous, false)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(result.DeletedResources) != 1 || !strings.Contains(result.DeletedResources[0], "lb-pool") {
		t.Fatalf("expected to delete lb-pool, deleted %v", result.DeletedResources)
	}
	if !f.ran("delete ciliumloadbalancerippool lb-pool") {
		t.Errorf("orphan was not deleted, ran: %v", f.calls)
	}
	if f.ran("delete configmap keep-me") {
		t.Error("deleted a resource that is still configured")
	}
}

// GetStatus reads several kubectl outputs; a failing Exec used to panic.
func TestGetStatusSurvivesExecErrors(t *testing.T) {
	f := &fakeExec{responses: []fakeResponse{
		{match: "is-active", result: ok("active")},
		{match: "kubectl get", err: errors.New("ssh: connection lost")},
	}}

	status, err := NewReconciler().GetStatus(context.Background(), f)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(status.Nodes) != 0 || len(status.HelmReleases) != 0 || len(status.IPPools) != 0 {
		t.Errorf("expected empty status, got %+v", status)
	}
}

package state

import (
	"context"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// A missing file must report DriftStatusMissing. It used to report
// content drift, because the hash command was piped through `cut` and the
// pipeline's exit status came from cut (always 0) instead of sha256sum.
func TestCheckDriftMissingFile(t *testing.T) {
	client := ssh.NewMockClient()
	client.RegisterCommandOutput("sha256sum /etc/gone.conf", "", 1)

	results, err := NewManager().CheckDrift(context.Background(), client, map[string]FileState{
		"/etc/gone.conf": {Path: "/etc/gone.conf", Hash: "abc", Mode: "0644", Owner: "root", Group: "root"},
	})
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != DriftStatusMissing {
		t.Errorf("expected %q, got %q", DriftStatusMissing, results[0].Status)
	}

	// The hash must not be gathered through a pipeline, or the exit status is
	// masked again.
	for _, cmd := range client.ExecLog {
		if strings.HasPrefix(cmd, "sha256sum") && strings.Contains(cmd, "|") {
			t.Errorf("hash command must not be a pipeline: %q", cmd)
		}
	}
}

// sha256sum exiting 0 with no output (seen when the path is a directory on
// some coreutils builds) is missing, not an empty-hash content change.
func TestCheckDriftEmptyHashOutput(t *testing.T) {
	client := ssh.NewMockClient()
	client.RegisterCommandOutput("sha256sum /etc/empty.conf", "\n", 0)

	results, err := NewManager().CheckDrift(context.Background(), client, map[string]FileState{
		"/etc/empty.conf": {Path: "/etc/empty.conf", Hash: "abc"},
	})
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if results[0].Status != DriftStatusMissing {
		t.Errorf("expected %q, got %q", DriftStatusMissing, results[0].Status)
	}
}

// The hash is the first field of "<hash>  <path>" output, and a file that
// matches is not drift.
func TestCheckDriftParsesHashAndPermissions(t *testing.T) {
	client := ssh.NewMockClient()
	client.RegisterCommandOutput("sha256sum /etc/ok.conf", "deadbeef  /etc/ok.conf\n", 0)
	client.RegisterCommandOutput("stat -c '%a %U %G' /etc/ok.conf", "644 root root\n", 0)

	results, err := NewManager().CheckDrift(context.Background(), client, map[string]FileState{
		"/etc/ok.conf": {Path: "/etc/ok.conf", Hash: "deadbeef", Mode: "644", Owner: "root", Group: "root"},
	})
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if results[0].Actual.Hash != "deadbeef" {
		t.Errorf("hash = %q, want deadbeef", results[0].Actual.Hash)
	}
	if results[0].Status != DriftStatusOK {
		t.Errorf("expected %q, got %q", DriftStatusOK, results[0].Status)
	}
}

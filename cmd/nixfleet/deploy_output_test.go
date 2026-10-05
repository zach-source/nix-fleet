package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/apply"
)

// The per-host lines `nixfleet apply` prints while deploying now come from the
// progress callback rather than from inline prints, so keep them pinned.
func TestApplyProgressOutput(t *testing.T) {
	result := &apply.DeployResult{Closure: "/nix/store/abc-system"}

	got := captureStdout(t, func() {
		applyProgress(apply.PhaseBuild, nil)
		applyProgress(apply.PhaseCopy, result)
		applyProgress(apply.PhaseConnect, result)
		applyProgress(apply.PhaseActivate, result)
		applyProgress(apply.PhaseState, result)
	})

	want := "  Built: /nix/store/abc-system\n  Copying closure...\n  Activating...\n"
	if got != want {
		t.Errorf("progress output =\n%q\nwant\n%q", got, want)
	}
}

// The build phase arrives before there is a result, so it must not be
// dereferenced.
func TestApplyProgressBuildPhaseHasNoResult(t *testing.T) {
	out := captureStdout(t, func() { applyProgress(apply.PhaseBuild, nil) })
	if strings.TrimSpace(out) != "" {
		t.Errorf("build phase printed %q", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	fn()
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured output: %v", err)
	}
	return string(out)
}

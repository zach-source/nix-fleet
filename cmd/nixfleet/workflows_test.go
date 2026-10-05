package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is this module's parent-parent: the Go module lives at cmd/nixfleet.
const repoRoot = "../.."

// goVersionRe matches the setup-go pin, e.g. `go-version: '1.25'`.
var goVersionRe = regexp.MustCompile(`go-version:\s*'?"?([0-9]+\.[0-9]+)`)

// TestWorkflowGoVersionMatchesGoMod guards the drift that shipped release.yml
// on Go 1.22 while go.mod required 1.25 — the release build would have failed
// on the next tag push, and nothing in CI would have caught it beforehand.
func TestWorkflowGoVersionMatchesGoMod(t *testing.T) {
	want := goModVersion(t)

	// pkgs/nixfleet sets src to cmd/nixfleet alone, so checkPhase inside a Nix
	// build has no repo root to look at. Skip there rather than fail; the CI
	// Test job and local `go test ./...` both run from a full checkout.
	dir := filepath.Join(repoRoot, ".github/workflows")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("%s absent; running outside a full checkout", dir)
	}

	workflows, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	if len(workflows) == 0 {
		t.Fatalf("%s has no *.yml; did the workflows move?", dir)
	}

	checked := 0
	for _, wf := range workflows {
		body, err := os.ReadFile(wf)
		if err != nil {
			t.Fatalf("read %s: %v", wf, err)
		}
		for _, m := range goVersionRe.FindAllStringSubmatch(string(body), -1) {
			checked++
			if m[1] != want {
				t.Errorf("%s pins Go %s, but cmd/nixfleet/go.mod requires %s",
					filepath.Base(wf), m[1], want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no go-version pins found in any workflow; the regex or the workflows changed")
	}
}

// goModVersion returns the major.minor of the `go` directive in go.mod.
func goModVersion(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go ")
		if !ok {
			continue
		}
		// go.mod may carry a patch ("1.25.0"); setup-go pins major.minor.
		parts := strings.SplitN(strings.TrimSpace(rest), ".", 3)
		if len(parts) < 2 {
			t.Fatalf("unparseable go directive: %q", line)
		}
		return parts[0] + "." + parts[1]
	}
	t.Fatal("no `go` directive in go.mod")
	return ""
}

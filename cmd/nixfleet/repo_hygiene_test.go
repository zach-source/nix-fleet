package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoBenchmarkArtifactsTracked guards against re-committing SWE-bench
// harness output. ~6 MB of logs/run_evaluation/ and openai__*.json prediction
// files were tracked at one point; .gitignore alone does not stop `git add -f`
// or a pattern that quietly stops matching.
//
// The question is specifically "is it *tracked*", not "is it on disk" — a
// developer running the harness locally should see no failure here.
func TestNoBenchmarkArtifactsTracked(t *testing.T) {
	// Same reason as TestWorkflowGoVersionMatchesGoMod: the Nix build's src is
	// cmd/nixfleet alone, so there is no work tree to ask git about.
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); os.IsNotExist(err) {
		t.Skip("no .git at repo root; running outside a full checkout")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	patterns := []string{"logs/", "openai__*.json"}
	out, err := exec.Command("git", append([]string{"-C", repoRoot, "ls-files", "--"}, patterns...)...).Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}

	tracked := strings.Fields(string(out))
	if len(tracked) > 0 {
		shown := tracked
		if len(shown) > 5 {
			shown = shown[:5]
		}
		t.Errorf("%d benchmark artifact(s) tracked, e.g. %v — these belong in .gitignore, with any result worth keeping summarised in FLEET.md",
			len(tracked), shown)
	}
}

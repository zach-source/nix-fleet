package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The version is stated in three places that have no way of checking each
// other. They drifted: the package derivation and the flake both said 0.1.0
// long after v0.1.5 was tagged and documented, so `nixfleet --version` and
// `nixfleet.meta` reported a release that was five tags stale.
var versionSites = []struct {
	name string
	path string
	re   *regexp.Regexp
}{
	{"pkgs/nixfleet/default.nix", "pkgs/nixfleet/default.nix",
		regexp.MustCompile(`(?m)^\s*version = "([0-9]+\.[0-9]+\.[0-9]+)";`)},
	{"flake.nix gitTag", "flake.nix",
		regexp.MustCompile(`gitTag = if self \? rev then "v([0-9]+\.[0-9]+\.[0-9]+)"`)},
	{"CLAUDE.md Current Version", "CLAUDE.md",
		regexp.MustCompile(`(?m)^- \*\*v([0-9]+\.[0-9]+\.[0-9]+)\*\* - `)},
}

func TestVersionIsConsistent(t *testing.T) {
	// Same reason as TestWorkflowGoVersionMatchesGoMod: the Nix build's src is
	// cmd/nixfleet alone, so none of these files are reachable there.
	if _, err := os.Stat(filepath.Join(repoRoot, "flake.nix")); os.IsNotExist(err) {
		t.Skip("no flake.nix at repo root; running outside a full checkout")
	}

	var want, wantFrom string
	for _, site := range versionSites {
		body, err := os.ReadFile(filepath.Join(repoRoot, site.path))
		if err != nil {
			t.Fatalf("read %s: %v", site.path, err)
		}
		m := site.re.FindSubmatch(body)
		if m == nil {
			t.Errorf("%s: no version found; the file was restructured and this guard is now blind", site.name)
			continue
		}
		got := string(m[1])
		if want == "" {
			want, wantFrom = got, site.name
			continue
		}
		if got != want {
			t.Errorf("%s says %s, but %s says %s", site.name, got, wantFrom, want)
		}
	}
}

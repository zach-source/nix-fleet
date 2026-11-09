package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot is this module's parent-parent: the Go module lives at cmd/nixfleet.
const repoRoot = "../.."

func TestWorkflowActionPinsAndPermissions(t *testing.T) {
	dir := filepath.Join(repoRoot, ".github/workflows")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("%s absent; running outside a full checkout", dir)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find workflows: %v (%d files)", err, len(files))
	}
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Permissions map[string]string `yaml:"permissions"`
				Jobs        map[string]struct {
					Permissions map[string]string `yaml:"permissions"`
					Steps       []struct {
						Uses string            `yaml:"uses"`
						With map[string]string `yaml:"with"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(body, &workflow); err != nil {
				t.Fatal(err)
			}
			if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
				t.Error("workflow must default to contents: read only")
			}
			for name, job := range workflow.Jobs {
				if filepath.Base(file) == "release.yml" && name == "release" {
					if len(job.Permissions) != 1 || job.Permissions["contents"] != "write" {
						t.Error("release job requires contents: write only")
					}
				} else {
					for scope, level := range job.Permissions {
						if scope != "contents" || level != "read" {
							t.Errorf("%s has unnecessary %s: %s", name, scope, level)
						}
					}
				}
				if name == "update-packages" && (job.Permissions == nil || len(job.Permissions) != 0) {
					t.Error("package dispatch must disable the repository GITHUB_TOKEN permissions")
				}
				dispatches := map[string]bool{}
				for _, step := range job.Steps {
					if step.Uses != "" && !pinned.MatchString(step.Uses) {
						t.Errorf("%s uses mutable action %q", name, step.Uses)
					}
					if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != "false" {
						t.Errorf("%s persists checkout credentials", name)
					}
					if strings.HasPrefix(step.Uses, "peter-evans/repository-dispatch@") {
						if name != "update-packages" || step.With["token"] != "${{ secrets.PACKAGES_TOKEN }}" || step.With["event-type"] != "nixfleet-release" {
							t.Errorf("unexpected dispatch scope/config in %s", name)
						}
						dispatches[step.With["repository"]] = true
					}
				}
				if name == "update-packages" && (len(dispatches) != 2 || !dispatches["zach-source/homebrew-tap"] || !dispatches["zach-source/nix-packages"]) {
					t.Error("both package repositories must still receive release dispatches")
				}
			}
		})
	}
}

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

var (
	devenvGoRe      = regexp.MustCompile(`(?m)^\s*goVersion\s*=\s*"([0-9]+\.[0-9]+)";`)
	devenvLintRe    = regexp.MustCompile(`(?m)^\s*golangciLintVersion\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)";`)
	goFloorRe       = regexp.MustCompile(`versionAtLeast\s+(?:pkgs\.)?go\.version\s+"([0-9.]+)"`)
	ciLintVersionRe = regexp.MustCompile(`(?m)^\s*version:\s*v([0-9]+\.[0-9]+\.[0-9]+)\s*$`)

	devenvNixpkgsRe = regexp.MustCompile(`(?m)^  nixpkgs:\n\s+url:\s*\S*[?&]rev=([0-9a-f]{40})\s*$`)
)

// TestDevenvPinsMatchCI makes sure the Go and golangci-lint pins in devenv.nix
// equal go.mod and the golangci-lint version in ci.yml.
func TestDevenvPinsMatchCI(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot, ".github/workflows")); os.IsNotExist(err) {
		t.Skipf("workflows absent; running outside a full checkout")
	}
	read := func(rel string) string {
		path := filepath.Join(repoRoot, rel)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(b)
	}
	one := func(re *regexp.Regexp, src, what string) string {
		m := re.FindAllStringSubmatch(src, -1)
		if len(m) != 1 {
			t.Fatalf("%s: want exactly one match of %s, got %d", what, re, len(m))
		}
		return m[0][1]
	}
	devenv := read("devenv.nix")
	if got, want := one(devenvGoRe, devenv, "devenv.nix goVersion"), goModVersion(t); got != want {
		t.Errorf("devenv.nix goVersion = %s, go.mod = %s", got, want)
	}
	floorDevenv := one(goFloorRe, devenv, "devenv.nix versionAtLeast floor")
	if floorPkg := one(goFloorRe, read("pkgs/nixfleet/default.nix"), "pkgs/nixfleet/default.nix versionAtLeast floor"); floorDevenv != floorPkg {
		t.Errorf("Go floor: devenv.nix = %s, pkgs/nixfleet/default.nix = %s", floorDevenv, floorPkg)
	}
	got := one(devenvLintRe, devenv, "devenv.nix golangciLintVersion")
	if want := one(ciLintVersionRe, read(".github/workflows/ci.yml"), "ci.yml version"); got != want {
		t.Errorf("devenv.nix golangciLintVersion = %s, ci.yml = %s", got, want)
	}

	var lock struct {
		Nodes map[string]struct {
			Locked struct {
				Rev string `json:"rev"`
			} `json:"locked"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(read("flake.lock")), &lock); err != nil {
		t.Fatalf("parse flake.lock: %v", err)
	}
	gotRev := one(devenvNixpkgsRe, read("devenv.yaml"), "devenv.yaml nixpkgs rev")
	if want := lock.Nodes["nixpkgs"].Locked.Rev; want == "" || gotRev != want {
		t.Errorf("devenv.yaml nixpkgs rev = %s, flake.lock nixpkgs rev = %q", gotRev, want)
	}
}

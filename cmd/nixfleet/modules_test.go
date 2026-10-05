package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// importRe matches a module import path in a host file, e.g.
// `    ../modules/k0s.nix`.
var importRe = regexp.MustCompile(`\.\./modules/([a-z0-9-]+\.nix)`)

// selfImportRe matches a module importing a sibling, e.g. `./model-watchdog.nix`
// inside modules/llm-inference.nix.
var selfImportRe = regexp.MustCompile(`imports = \[[^\]]*\./([a-z0-9-]+\.nix)`)

// reachedByFlake are imported by flake.nix directly rather than by a host.
var reachedByFlake = map[string]bool{"nix-config.nix": true}

// knownUnimported are modules with no importer today, kept deliberately. Each
// needs a reason, because the default assumption for an unimported module is
// that it is dead and should go.
//
// Deleting one of these is a judgement call, not a mechanical one: an
// unimported module may still be live on a host that was applied from an
// unmerged branch, or may be the only implementation of something the fleet
// docs describe as running.
var knownUnimported = map[string]string{
	"dolt.nix":    "the fleet's dolt remote runs on gti; the module is not imported on main",
	"juicefs.nix": "paired with the `nixfleet juicefs` command and internal/juicefs",
	"pki.nix":     "nothing reads nixfleet.pki; cert deployment is Go-side in internal/pki",
	"base.nix":    "superseded by base-packages.nix, but `base` is an inventory role name",
	"nginx.nix":   "`nginx` is an inventory role name in inventory/example.yaml",
}

// TestNoUnexplainedUnimportedModules fails when a module is imported by nothing
// and is not on the knownUnimported list. The point is to force the question at
// review time instead of letting modules/ silently accumulate dead files —
// modules/vllm.nix sat unimported long after modules/dspark-dsv4.nix replaced it.
//
// It fails in the other direction too: once a listed module gains an importer,
// its entry is stale and has to go.
func TestNoUnexplainedUnimportedModules(t *testing.T) {
	modulesDir := filepath.Join(repoRoot, "modules")
	if _, err := os.Stat(modulesDir); os.IsNotExist(err) {
		t.Skip("no modules/ at repo root; running outside a full checkout")
	}

	imported := collectImports(t)

	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		t.Fatalf("read modules/: %v", err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".nix") {
			continue
		}
		seen[name] = true

		_, excused := knownUnimported[name]
		switch {
		case imported[name] || reachedByFlake[name]:
			if excused {
				t.Errorf("modules/%s now has an importer; drop its knownUnimported entry", name)
			}
		case !excused:
			t.Errorf("modules/%s is imported by nothing. Delete it, or add it to knownUnimported with the reason it stays.", name)
		}
	}

	for name := range knownUnimported {
		if !seen[name] {
			t.Errorf("knownUnimported lists modules/%s, which does not exist; drop the entry", name)
		}
	}
}

// collectImports returns every module filename imported by a host file or by
// another module.
func collectImports(t *testing.T) map[string]bool {
	t.Helper()

	imported := map[string]bool{}
	for _, pattern := range []string{"hosts/*.nix", "hosts/*/*.nix", "modules/*.nix", "netboot/*.nix"} {
		files, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		for _, f := range files {
			body, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			// Comments mention module paths constantly ("see modules/x.nix"),
			// so only count lines that are not comments.
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				for _, m := range importRe.FindAllStringSubmatch(line, -1) {
					imported[m[1]] = true
				}
			}
			for _, m := range selfImportRe.FindAllStringSubmatch(string(body), -1) {
				imported[m[1]] = true
			}
		}
	}
	return imported
}

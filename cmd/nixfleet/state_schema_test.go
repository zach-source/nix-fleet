package main

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/state"
)

// state.json is written from three places outside Go: the Ubuntu activation
// script and the two bootstrap paths. They used to emit a second, camelCase
// schema ("generation", "manifestHash", "lastApply", ...) that the Go
// state.HostState reader could not see, so every activation reset the
// controller's view of the host to zero. These tests fail if a writer
// reintroduces a key that HostState does not understand.
func TestStateFileWritersUseHostStateSchema(t *testing.T) {
	known := hostStateJSONTags()

	for _, w := range []struct {
		file   string
		marker string
	}{
		{"../../backends/ubuntu/compile.nix", `cat > "$STATE_FILE.new" << STATE_EOF`},
		{"../../scripts/bootstrap-ubuntu.sh", `cat >/var/lib/nixfleet/state.json <<EOF`},
		{"../../installer/nixfleet-late-commands.sh", `cat > /var/lib/nixfleet/state.json <<STATE`},
	} {
		body := stateJSONBody(t, w.file, w.marker)
		keys := regexp.MustCompile(`"(\w+)"\s*:`).FindAllStringSubmatch(body, -1)
		if len(keys) == 0 {
			t.Errorf("%s: found no JSON keys in the state write; marker moved?", w.file)
			continue
		}
		for _, m := range keys {
			if !known[m[1]] {
				t.Errorf("%s writes state.json key %q, which is not a field of state.HostState",
					w.file, m[1])
			}
		}
	}
}

// The bootstrap seed must parse as a HostState. This is the literal the two
// bootstrap scripts write, with the command substitutions filled in.
func TestBootstrapSeedUnmarshalsIntoHostState(t *testing.T) {
	const seed = `{
  "hostname": "gtr-150",
  "base": "ubuntu",
  "current_generation": 0,
  "manifest_hash": "",
  "reboot_required": false,
  "state_version": 1,
  "updated_at": "2026-10-04T12:00:00-07:00"
}`

	var hs state.HostState
	if err := json.Unmarshal([]byte(seed), &hs); err != nil {
		t.Fatalf("bootstrap seed does not parse as HostState: %v", err)
	}
	if hs.Hostname != "gtr-150" || hs.Base != "ubuntu" || hs.StateVersion != 1 {
		t.Errorf("seed fields did not populate: %+v", hs)
	}
}

// What the activation script writes must land in the fields the controller and
// `nixfleet plan` read, or the host reads as a NEW DEPLOYMENT forever.
func TestActivationWriteUnmarshalsIntoHostState(t *testing.T) {
	const written = `{
  "hostname": "gtr-150",
  "base": "ubuntu",
  "current_generation": 42,
  "manifest_hash": "cafebabe",
  "last_apply": "2026-10-04T12:00:00-07:00",
  "state_version": 1,
  "updated_at": "2026-10-04T12:00:00-07:00"
}`

	var hs state.HostState
	if err := json.Unmarshal([]byte(written), &hs); err != nil {
		t.Fatalf("activation write does not parse as HostState: %v", err)
	}
	if hs.CurrentGeneration != 42 {
		t.Errorf("CurrentGeneration = %d, want 42", hs.CurrentGeneration)
	}
	if hs.ManifestHash != "cafebabe" {
		t.Errorf("ManifestHash = %q, want cafebabe", hs.ManifestHash)
	}
	if hs.LastApply.IsZero() {
		t.Error("LastApply did not parse")
	}
}

// The activation script must merge rather than overwrite: the controller owns
// the managed-file hashes, k0s inventory and OS info in the same file.
func TestActivationScriptMergesState(t *testing.T) {
	src, err := readRepoFile(t, "../../backends/ubuntu/compile.nix")
	if err != nil {
		t.Fatalf("reading compile.nix: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `"$JQ" -s '.[0] * .[1]'`) {
		t.Error("activation script no longer merges state.json into the existing state")
	}
	if strings.Contains(body, `cat > "$NIXFLEET_STATE/state.json"`) {
		t.Error("activation script overwrites state.json, dropping controller-owned fields")
	}
}

// readRepoFile reads a file from outside the Go module. The nix build copies
// only cmd/nixfleet into its sandbox, so these files are absent there; the
// checks run from a full checkout (`go test ./...`, and CI's Test job).
func readRepoFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("%s is not in this build's source tree", path)
	}
	return b, err
}

// hostStateJSONTags returns the set of JSON field names HostState accepts.
func hostStateJSONTags() map[string]bool {
	tags := make(map[string]bool)
	t := reflect.TypeOf(state.HostState{})
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			tags[name] = true
		}
	}
	return tags
}

// stateJSONBody returns the heredoc body that follows marker, up to and
// including its closing brace.
func stateJSONBody(t *testing.T, file, marker string) string {
	t.Helper()
	src, err := readRepoFile(t, file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	i := strings.Index(string(src), marker)
	if i < 0 {
		t.Fatalf("%s: marker %q not found", file, marker)
	}
	rest := string(src)[i+len(marker):]
	for n, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "}" {
			return strings.Join(strings.Split(rest, "\n")[:n+1], "\n")
		}
	}
	t.Fatalf("%s: no closing brace after %q", file, marker)
	return ""
}

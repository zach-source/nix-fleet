package pullmode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPullRetryAndLockLifetime(t *testing.T) {
	for _, retry := range []bool{false, true} {
		mode := "legacy"
		if retry {
			mode = "retry"
		}
		script, err := renderPullScript(Config{RepoPath: "/unused", HostName: "fixture", Branch: "main", RetryFailedApply: retry, MutationLockTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "pull")
		if err := os.WriteFile(path, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "testdata/pull-mode.sh", path, mode)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("pull regression: %v\n%s", err, out)
		}
	}
}

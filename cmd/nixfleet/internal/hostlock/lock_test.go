package hostlock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContenderTimesOutWithoutEntering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mutation.lock")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", Wrap("echo entered", path, time.Second)).CombinedOutput()
	if err == nil || strings.Contains(string(out), "entered") {
		t.Fatalf("contender entered: %s, %v", out, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("contender removed owner's lock")
	}
}
func TestReleasesAfterFailureAndAllowsNestedActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mutation.lock")
	script := Wrap(Wrap("exit 7", path, time.Second), path, time.Second)
	err := exec.Command("sh", "-c", script).Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
		t.Fatalf("nested activation: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("lock remains after failure")
	}
}
func TestDefaultDoesNotChangeCommand(t *testing.T) {
	if Wrap("legacy command", MutationPath, 0) != "legacy command" {
		t.Fatal("default changed")
	}
}

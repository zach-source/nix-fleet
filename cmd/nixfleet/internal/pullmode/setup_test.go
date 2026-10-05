package pullmode

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// failingExec returns Exec's real failure shape: a nil result alongside the
// error.
type failingExec struct{ calls []string }

func (f *failingExec) Exec(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	f.calls = append(f.calls, cmd)
	return nil, errors.New("ssh: connection lost")
}

func (f *failingExec) ExecSudo(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	return f.Exec(ctx, "sudo "+cmd)
}

// The repo check used to be `result, _ := client.ExecSudo(...)` followed by
// result.ExitCode, which panics when the connection drops.
func TestSetupRepositoryReportsAFailedCheck(t *testing.T) {
	f := &failingExec{}
	err := (&Installer{}).setupRepository(context.Background(), f, Config{RepoPath: "/etc/nixfleet/repo"})
	if err == nil {
		t.Fatal("expected an error when the repo check fails")
	}
	if !strings.Contains(err.Error(), "existing repository") {
		t.Errorf("unexpected error: %v", err)
	}
	// It must not have gone on to clone or reset anything.
	for _, c := range f.calls[1:] {
		t.Errorf("ran %q after the check failed", c)
	}
}

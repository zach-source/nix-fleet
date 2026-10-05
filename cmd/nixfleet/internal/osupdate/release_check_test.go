package osupdate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// stubExec answers by command substring; unmatched commands fail the way Exec
// really does, with a nil result alongside the error.
type stubExec struct{ replies map[string]string }

func (s stubExec) Exec(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	for match, out := range s.replies {
		if strings.Contains(cmd, match) {
			return &ssh.ExecResult{Stdout: out}, nil
		}
	}
	return nil, errors.New("ssh: connection lost")
}

func (s stubExec) ExecSudo(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	return s.Exec(ctx, cmd)
}

// `check, _ := client.Exec(...)` then check.Stdout panicked when the host went
// away between the tool probe and the upgrade check.
func TestCheckReleaseInfoReportsAFailedUpgradeCheck(t *testing.T) {
	client := stubExec{replies: map[string]string{
		"/etc/os-release":               "24.04|noble\n",
		"command -v do-release-upgrade": "yes\n",
	}}

	_, err := (&Updater{}).CheckReleaseInfo(context.Background(), client)
	if err == nil {
		t.Fatal("expected an error when the upgrade check fails")
	}
	if !strings.Contains(err.Error(), "release upgrade") {
		t.Errorf("unexpected error: %v", err)
	}
}

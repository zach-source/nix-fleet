package state

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

type localStateExec struct {
	dir       string
	failWrite bool
	writes    int
}

func (f *localStateExec) Exec(ctx context.Context, command string) (*ssh.ExecResult, error) {
	return f.ExecSudo(ctx, command)
}
func (f *localStateExec) ExecSudo(ctx context.Context, command string) (*ssh.ExecResult, error) {
	command = strings.ReplaceAll(command, StateDir, f.dir)
	if strings.Contains(command, "<<") {
		f.writes++
		if f.failWrite {
			bin := filepath.Join(f.dir, "bin")
			_ = os.MkdirAll(bin, 0700)
			for _, name := range []string{"tee", "cat"} {
				_ = os.WriteFile(filepath.Join(bin, name), []byte(`#!/bin/sh
case "$0" in */tee) printf partial > "$1";; *) printf partial;; esac
exit 1
`), 0700)
			}
			command = "PATH=" + ssh.ShellQuote(bin) + ":$PATH\n" + command
		}
	}
	out, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			return nil, err
		}
	}
	return &ssh.ExecResult{Stdout: string(out), ExitCode: code}, nil
}

func TestStateReadErrorsDoNotResetDeployment(t *testing.T) {
	for _, body := range []string{"{broken", `{"state_version":99}`, `null`} {
		f := &localStateExec{dir: t.TempDir()}
		path := filepath.Join(f.dir, "state.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := NewManager().UpdateAfterApply(context.Background(), f, "ubuntu", "/nix/store/new", "hash", 2, time.Second); err == nil {
			t.Errorf("accepted invalid state %q", body)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body || f.writes != 0 {
			t.Errorf("overwrote unreadable state %q", body)
		}
	}
}

func TestStateWriteFailurePreservesOldDocument(t *testing.T) {
	f := &localStateExec{dir: t.TempDir(), failWrite: true}
	path := filepath.Join(f.dir, "state.json")
	const old = `{"state_version":1,"store_path":"/nix/store/old"}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := NewManager().WriteState(context.Background(), f, NewHostState("fixture", "ubuntu")); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != old {
		t.Fatalf("old state changed: %q", got)
	}
}

func TestMissingStateBootstrapsAndWrites(t *testing.T) {
	f := &localStateExec{dir: t.TempDir()}
	m := NewManager()
	if err := m.UpdateAfterApply(context.Background(), f, "ubuntu", "/nix/store/new", "hash", 2, time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := m.ReadState(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got.StorePath != "/nix/store/new" {
		t.Fatalf("state=%+v", got)
	}
}

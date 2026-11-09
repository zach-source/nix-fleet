package state

import (
	"context"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type publicationExec struct{ *localStateExec }

func (f publicationExec) ExecSudo(ctx context.Context, command string) (*ssh.ExecResult, error) {
	command = strings.ReplaceAll(command, "/run/current-system", filepath.Join(f.dir, "current"))
	command = strings.ReplaceAll(command, "/nix/var/nix/profiles/system", filepath.Join(f.dir, "system"))
	return f.localStateExec.ExecSudo(ctx, command)
}
func TestDelayedPublisherCannotDescribeSupersededClosure(t *testing.T) {
	for _, tc := range []struct {
		active, link string
		success      bool
	}{{"/nix/store/old", "system-42-link", true}, {"/nix/store/new", "system-42-link", false}, {"/nix/store/old", "system-43-link", false}} {
		active := tc.active
		f := publicationExec{&localStateExec{dir: t.TempDir()}}
		if err := os.Symlink(active, filepath.Join(f.dir, "current")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(tc.link, filepath.Join(f.dir, "system")); err != nil {
			t.Fatal(err)
		}
		m := NewManager()
		m.MutationLockTimeout = time.Second
		err := m.UpdateAfterApply(context.Background(), f, "nixos", "/nix/store/old", "hash", 42, time.Second)
		if tc.success && err != nil {
			t.Fatal(err)
		}
		if !tc.success && err == nil {
			t.Fatal("published obsolete closure after another deployment completed")
		}
	}
}

func TestPublicationHonorsTheMutationLock(t *testing.T) {
	f := publicationExec{&localStateExec{dir: t.TempDir()}}
	for path, target := range map[string]string{"current": "/nix/store/old", "system": "system-42-link"} {
		if err := os.Symlink(target, filepath.Join(f.dir, path)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(f.dir, "mutation.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	m.MutationLockTimeout = time.Second
	if err := m.UpdateAfterApply(context.Background(), f, "nixos", "/nix/store/old", "hash", 42, time.Second); err == nil {
		t.Fatal("published during a concurrent mutation")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "state.json")); !os.IsNotExist(err) {
		t.Fatal("contender wrote state")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "mutation.lock")); err != nil {
		t.Fatal("contender removed owner's lock")
	}
}

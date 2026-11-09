package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandFailureReporting(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, "inventory.yaml")
	if err := os.WriteFile(inv, []byte("hosts:\n  test:\n    base: ubuntu\n    addr: 127.0.0.1\n    ssh_port: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nix"), []byte("#!/bin/sh\necho 'fixture build failure' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, command := range []string{"apply", "rollback"} {
		for _, strict := range []bool{false, true} {
			t.Run(command+"/strict="+map[bool]string{false: "false", true: "true"}[strict], func(t *testing.T) {
				cmd := rootCmd()
				args := []string{"--inventory", inv, "--flake", dir, command}
				if command == "apply" {
					args = append(args, "--skip-preflight", "--skip-health")
				}
				if strict {
					args = append(args, "--fail-on-error")
				}
				cmd.SetArgs(args)
				// Cancellation prevents rollback from attempting even a loopback connection.
				ctx := context.Background()
				if command == "rollback" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				err := cmd.ExecuteContext(ctx)
				if strict {
					if err == nil || !strings.Contains(err.Error(), "1 host(s) failed") {
						t.Fatalf("want host failure, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("legacy mode: %v", err)
				}
			})
		}
	}
}

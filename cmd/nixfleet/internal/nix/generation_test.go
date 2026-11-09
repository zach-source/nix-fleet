package nix

import (
	"context"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"strings"
	"testing"
)

type generationExec struct {
	command string
	exit    int
}

func (f *generationExec) Exec(_ context.Context, command string) (*ssh.ExecResult, error) {
	f.command = command
	return &ssh.ExecResult{ExitCode: f.exit}, nil
}
func (f *generationExec) ExecSudo(ctx context.Context, command string) (*ssh.ExecResult, error) {
	return f.Exec(ctx, command)
}

func TestNixOSGenerationOwnership(t *testing.T) {
	for _, managed := range []bool{false, true} {
		d := &Deployer{ManageNixOSProfile: managed}
		for _, action := range []string{"", "switch", "boot", "test", "dry-activate"} {
			f := &generationExec{}
			if err := d.activateNixOS(context.Background(), f, &HostClosure{StorePath: "/nix/store/fixture"}, action); err != nil {
				t.Fatal(err)
			}
			persistent := action == "" || action == "switch" || action == "boot"
			if strings.Contains(f.command, "nix-env --profile /nix/var/nix/profiles/system --set /nix/store/fixture &&") != (managed && persistent) {
				t.Errorf("managed=%v action=%q: %s", managed, action, f.command)
			}
		}
		for _, gen := range []int{0, 7} {
			f := &generationExec{}
			if err := d.rollbackNixOS(context.Background(), f, gen); err != nil {
				t.Fatal(err)
			}
			if managed && gen == 7 && !strings.Contains(f.command, "--switch-generation 7 && /nix/var/nix/profiles/system/bin/switch-to-configuration switch") {
				t.Errorf("target profile not selected: %s", f.command)
			}
			if managed && gen == 0 && strings.Contains(f.command, "/run/current-system") {
				t.Errorf("activates old system before selecting previous generation: %s", f.command)
			}
		}
		f := &generationExec{exit: 1}
		if err := d.activateNixOS(context.Background(), f, &HostClosure{StorePath: "/nix/store/fixture"}, "switch"); err == nil {
			t.Fatal("lost activation failure")
		}
	}
}

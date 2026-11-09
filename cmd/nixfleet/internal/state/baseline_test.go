package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type baselineExec struct {
	baseline string
	written  string
}

func (f *baselineExec) Exec(ctx context.Context, cmd string) (*ssh.ExecResult, error) {
	return f.ExecSudo(ctx, cmd)
}
func (f *baselineExec) ExecSudo(_ context.Context, cmd string) (*ssh.ExecResult, error) {
	if strings.Contains(cmd, "nixfleet-managed-files.json") {
		return &ssh.ExecResult{Stdout: f.baseline}, nil
	}
	if strings.Contains(cmd, "<< 'NIXFLEET_STATE'") {
		f.written = strings.Split(strings.Split(cmd, "<< 'NIXFLEET_STATE'\n")[1], "\nNIXFLEET_STATE")[0]
		return &ssh.ExecResult{}, nil
	}
	return &ssh.ExecResult{Stdout: `{"state_version":1,"managed_files":{"/old":{"hash":"old"}}}`}, nil
}
func TestApplyRefreshesImmutableBaseline(t *testing.T) {
	for _, baseline := range []string{`{"/new":{"path":"/new","hash":"immutable","mode":"644","owner":"root","group":"root"}}`, `{}`, `null`} {
		f := &baselineExec{baseline: baseline}
		if err := NewManager().UpdateAfterApply(context.Background(), f, "/nix/store/fixture", "hash", 1, time.Second); err != nil {
			t.Fatal(err)
		}
		var got HostState
		if err := json.Unmarshal([]byte(f.written), &got); err != nil {
			t.Fatal(err)
		}
		if baseline == "null" {
			if _, ok := got.ManagedFiles["/old"]; !ok {
				t.Error("legacy closure lost baseline")
			}
			continue
		}
		if _, ok := got.ManagedFiles["/old"]; ok {
			t.Error("removed file remains in baseline")
		}
		if baseline != "{}" && got.ManagedFiles["/new"].Hash != "immutable" {
			t.Error("new immutable baseline missing")
		}
	}
}

func TestStoreSymlinkDriftUsesTargetMetadata(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "immutable")
	path := filepath.Join(dir, "link with spaces")
	if err := os.WriteFile(target, []byte("fixture"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	owner, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	group, err := exec.Command("id", "-gn").Output()
	if err != nil {
		t.Fatal(err)
	}
	expected := FileState{Hash: fmt.Sprintf("%x", sha256.Sum256([]byte("fixture"))), Mode: "444", Owner: strings.TrimSpace(string(owner)), Group: strings.TrimSpace(string(group)), FollowSymlink: true}
	got, err := NewManager().CheckDrift(context.Background(), &localStateExec{dir: dir}, map[string]FileState{path: expected})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != DriftStatusOK {
		t.Fatalf("store symlink falsely drifted: %+v", got)
	}
}

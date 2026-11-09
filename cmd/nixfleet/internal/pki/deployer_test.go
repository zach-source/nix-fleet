package pki

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

type fileExecutor struct {
	command string
	input   []byte
	result  ssh.ExecResult
	err     error
}

func (e *fileExecutor) Exec(_ context.Context, command string) (*ssh.ExecResult, error) {
	e.command = command
	return &e.result, e.err
}
func (e *fileExecutor) ExecSudoStdin(_ context.Context, command string, input []byte) (*ssh.ExecResult, error) {
	e.command, e.input = command, input
	return &e.result, e.err
}

func TestDeployFileContentPrivateStdin(t *testing.T) {
	key := []byte("synthetic-private-key\nwith special characters '$;\n")
	dest := filepath.Join(t.TempDir(), "host '$(touch injected).key")
	client := &fileExecutor{}
	if err := DeployFileContent(context.Background(), client, key, dest, "0600"); err != nil {
		t.Fatal(err)
	}
	if string(client.input) != string(key) {
		t.Fatal("private key was not transported over stdin")
	}
	if strings.Contains(client.command, string(key)) || strings.Contains(client.command, base64.StdEncoding.EncodeToString(key)) {
		t.Fatal("command contains private key material")
	}
	// Execute only the generated file-writing shell locally with synthetic content;
	// no SSH, sudo, or real private material is involved.
	run := func() {
		t.Helper()
		cmd := exec.Command("sh", "-c", client.command)
		cmd.Stdin = strings.NewReader(string(client.input))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("write failed: %v: %s", err, out)
		}
		got, err := os.ReadFile(dest)
		if err != nil || string(got) != string(key) {
			t.Fatalf("wrong content: %v", err)
		}
		info, err := os.Stat(dest)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("key mode is not private: %v", err)
		}
	}
	run()
	// Replacing an existing permissive key must not retain its mode.
	if err := os.Chmod(dest, 0644); err != nil {
		t.Fatal(err)
	}
	run()
	// A final-component symlink must be replaced, not followed.
	victim := filepath.Join(filepath.Dir(dest), "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dest); err != nil {
		t.Fatal(err)
	}
	run()
	got, _ := os.ReadFile(victim)
	if string(got) != "unchanged" {
		t.Fatal("followed destination symlink")
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 2 {
		t.Fatal("temporary file leaked")
	}
}

func TestDeployFileContentErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client fileExecutor
	}{
		{"remote exit", fileExecutor{result: ssh.ExecResult{ExitCode: 1}}},
		{"transport", fileExecutor{err: errors.New("transport failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := DeployFileContent(context.Background(), &tc.client, []byte("synthetic"), "/not/executed", "0600"); err == nil {
				t.Fatal("failure reported as success")
			}
		})
	}
}

func TestDeployFileContentCertificateAndFailedWrite(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ca.crt")
	for _, mode := range []string{"0644", "invalid-mode"} {
		client := &fileExecutor{}
		if err := DeployFileContent(context.Background(), client, []byte("synthetic certificate"), dest, mode); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", client.command)
		cmd.Stdin = strings.NewReader(string(client.input))
		err := cmd.Run()
		if (err != nil) != (mode == "invalid-mode") {
			t.Fatalf("mode %s: %v", mode, err)
		}
		info, err := os.Stat(dest)
		if err != nil || info.Mode().Perm() != 0644 {
			t.Fatalf("certificate mode changed: %v", err)
		}
		entries, _ := os.ReadDir(filepath.Dir(dest))
		if len(entries) != 1 {
			t.Fatal("failed write leaked temporary file")
		}
	}
}

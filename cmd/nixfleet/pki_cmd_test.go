package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The exported Secret manifest embeds tls.key, so the file must never be
// group- or world-readable, including when it overwrites a looser file.
func TestWriteK8sSecretFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca-secret.yaml")

	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeK8sSecretFile(path, []byte(`{"kind":"Secret"}`)); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("mode = %04o, want 0600", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"kind":"Secret"}` {
		t.Errorf("content = %q, want the new manifest", data)
	}
}

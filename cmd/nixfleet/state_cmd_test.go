package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/nixfleet/nixfleet/internal/nix"
)

func strptr(s string) *string { return &s }

func TestExpectedManagedFiles(t *testing.T) {
	text := "iscsi_tcp\n"
	textSum := sha256.Sum256([]byte(text))
	wantTextHash := hex.EncodeToString(textSum[:])

	// A source-backed file is hashed from its content on disk.
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.conf")
	srcContent := []byte("source-content\n")
	if err := os.WriteFile(srcPath, srcContent, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	srcSum := sha256.Sum256(srcContent)
	wantSrcHash := hex.EncodeToString(srcSum[:])

	declared := map[string]nix.DeclaredFile{
		"/etc/modules-load.d/iscsi_tcp.conf": {
			Text:         strptr(text),
			Mode:         "0644",
			Owner:        "root",
			Group:        "root",
			RestartUnits: []string{"iscsid.service"},
		},
		"/etc/from-source.conf": {
			Source: strptr(srcPath),
			Mode:   "0600",
			Owner:  "root",
			Group:  "root",
		},
		"/etc/empty.conf": {}, // neither text nor source -> skipped
	}

	got, skipped := expectedManagedFiles(declared)
	if len(skipped) != 0 {
		t.Fatalf("unexpected skipped files: %v", skipped)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 files (empty skipped), got %d", len(got))
	}

	fs := got["/etc/modules-load.d/iscsi_tcp.conf"]
	if fs.Hash != wantTextHash {
		t.Errorf("text hash = %s, want %s", fs.Hash, wantTextHash)
	}
	if fs.Mode != "644" || fs.Owner != "root" || fs.Group != "root" {
		t.Errorf("metadata mismatch (mode should be normalized to stat -c %%a form): %+v", fs)
	}
	if len(fs.RestartUnits) != 1 || fs.RestartUnits[0] != "iscsid.service" {
		t.Errorf("restartUnits = %v", fs.RestartUnits)
	}

	src := got["/etc/from-source.conf"]
	if src.Hash != wantSrcHash {
		t.Errorf("source hash = %s, want %s", src.Hash, wantSrcHash)
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"0644":  "644",
		"0755":  "755",
		"0600":  "600",
		"04755": "4755",
		"644":   "644",
		"0":     "0",
	}
	for in, want := range cases {
		if got := normalizeMode(in); got != want {
			t.Errorf("normalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
	// Unparseable input is returned unchanged.
	if got := normalizeMode("rwx"); got != "rwx" {
		t.Errorf("normalizeMode(rwx) = %q, want rwx", got)
	}
}

func TestExpectedManagedFilesMissingSource(t *testing.T) {
	declared := map[string]nix.DeclaredFile{
		"/etc/nope.conf": {Source: strptr("/nonexistent/path/xyz")},
	}
	got, skipped := expectedManagedFiles(declared)
	if len(got) != 0 {
		t.Fatalf("expected no hashable files, got %d", len(got))
	}
	if len(skipped) != 1 || skipped[0] != "/etc/nope.conf" {
		t.Fatalf("expected /etc/nope.conf skipped, got %v", skipped)
	}
}

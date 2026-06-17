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

	got, err := expectedManagedFiles(declared)
	if err != nil {
		t.Fatalf("expectedManagedFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 files (empty skipped), got %d", len(got))
	}

	fs := got["/etc/modules-load.d/iscsi_tcp.conf"]
	if fs.Hash != wantTextHash {
		t.Errorf("text hash = %s, want %s", fs.Hash, wantTextHash)
	}
	if fs.Mode != "0644" || fs.Owner != "root" || fs.Group != "root" {
		t.Errorf("metadata mismatch: %+v", fs)
	}
	if len(fs.RestartUnits) != 1 || fs.RestartUnits[0] != "iscsid.service" {
		t.Errorf("restartUnits = %v", fs.RestartUnits)
	}

	src := got["/etc/from-source.conf"]
	if src.Hash != wantSrcHash {
		t.Errorf("source hash = %s, want %s", src.Hash, wantSrcHash)
	}
}

func TestExpectedManagedFilesMissingSource(t *testing.T) {
	declared := map[string]nix.DeclaredFile{
		"/etc/nope.conf": {Source: strptr("/nonexistent/path/xyz")},
	}
	if _, err := expectedManagedFiles(declared); err == nil {
		t.Fatal("expected error for missing source file, got nil")
	}
}

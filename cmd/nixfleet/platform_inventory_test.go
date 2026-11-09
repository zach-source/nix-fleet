package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMixedInventorySelection(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, "hosts.yaml")
	data := "hosts:\n  mac:\n    base: darwin\n    addr: 127.0.0.1\n  nas:\n    base: synology\n    addr: 127.0.0.1\n"
	if err := os.WriteFile(inv, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"mac", "nas"} {
		cmd := rootCmd()
		cmd.SetArgs([]string{"-i", inv, "-H", host, "--dry-run", "apply"})
		err := cmd.ExecuteContext(context.Background())
		if host == "mac" && err != nil {
			t.Fatalf("Darwin blocked by mixed inventory: %v", err)
		}
		if host == "nas" && (err == nil || !strings.Contains(err.Error(), "synology apply")) {
			t.Fatalf("want explicit Synology capability error, got %v", err)
		}
	}
}

package nodestatus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredChecksDoNotImplyPassingWhenReportingUnchecked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "health.json"), []byte(`{"fixture":{"type":"command","command":"false"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Config{StateDir: dir, ReportUncheckedHealth: true})
	got := s.gatherHealthInfo()
	if got == nil || got.Summary != "unknown" || got.Checks["fixture"] != "defined" {
		t.Fatalf("declarations presented as passing: %+v", got)
	}
	legacy := NewServer(Config{StateDir: dir}).gatherHealthInfo()
	if legacy.Summary != "all_passing" {
		t.Fatal("default compatibility changed")
	}
}

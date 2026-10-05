package osupdate

import "testing"

func TestDefaultReleaseUpgradeConfig(t *testing.T) {
	c := DefaultReleaseUpgradeConfig()
	if c.MinFreeRootMB <= 0 {
		t.Errorf("expected positive MinFreeRootMB, got %d", c.MinFreeRootMB)
	}
	if c.LogPath == "" || c.Unit == "" {
		t.Errorf("expected non-empty LogPath and Unit, got %q / %q", c.LogPath, c.Unit)
	}
}

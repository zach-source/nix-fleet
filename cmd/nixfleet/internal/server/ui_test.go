package server

import (
	"os/exec"
	"testing"
)

func TestDashboardRendering(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("dashboard rendering regression requires Node.js: %v", err)
	}
	if output, err := exec.Command(node, "--test", "ui_test.cjs").CombinedOutput(); err != nil {
		t.Fatalf("dashboard rendering: %v\n%s", err, output)
	}
}

package preflight

import (
	"context"
	"github.com/nixfleet/nixfleet/internal/ssh"
	"testing"
)

func TestStoreVerificationRejectsRemoteFailure(t *testing.T) {
	client := ssh.NewMockClient()
	client.RegisterCommandOutput("sudo nix-store --verify --check-contents", "verification interrupted", 7)
	client.RegisterCommandOutput("nix --version", "nix fixture", 0)
	got := NewChecker().checkNixStore(context.Background(), client)
	if got.Passed {
		t.Fatal("failed store verification passed preflight")
	}
}

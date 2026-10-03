package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

// Loading an inventory must both expand ~/ and register the key under the
// host's ADDRESS — internal/ssh looks it up by address, since that is all a
// connection knows about the host it is dialling.
func TestLoadRegistersPinnedKeyByAddr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	yaml := `
hosts:
  pinned:
    addr: 10.0.0.1
    ssh_user: ztaylor
    ssh_key: ~/.ssh/odd-one-out
  plain:
    addr: 10.0.0.2
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	inv, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	t.Cleanup(func() {
		SetHostKey("10.0.0.1", "")
		SetHostKey("10.0.0.2", "")
	})

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	want := filepath.Join(home, ".ssh", "odd-one-out")

	if got := inv.Hosts["pinned"].SSHKey; got != want {
		t.Errorf("SSHKey = %q, want %q (~/ should be expanded)", got, want)
	}
	if got := HostKey("10.0.0.1"); got != want {
		t.Errorf("HostKey(addr) = %q, want %q", got, want)
	}

	// A host without ssh_key must stay on the fleet defaults, not inherit a
	// neighbour's key.
	if got := inv.Hosts["plain"].SSHKey; got != "" {
		t.Errorf("unpinned host got SSHKey %q, want empty", got)
	}
	if got := HostKey("10.0.0.2"); got != "" {
		t.Errorf("HostKey for unpinned host = %q, want empty", got)
	}
}

func TestSetHostKeyClears(t *testing.T) {
	SetHostKey("10.0.0.9", "/tmp/k")
	if HostKey("10.0.0.9") != "/tmp/k" {
		t.Fatal("key not stored")
	}
	SetHostKey("10.0.0.9", "")
	if got := HostKey("10.0.0.9"); got != "" {
		t.Errorf("HostKey after clear = %q, want empty", got)
	}
}

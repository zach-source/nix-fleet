package secrets

import (
	"strings"
	"testing"
)

// The decrypted secret is streamed over stdin. The remote command is built
// from the destination path alone, so it cannot carry the value — argv is
// readable by every user on the remote host via /proc.
func TestWriteSecretCommandReadsStdin(t *testing.T) {
	cmd := writeSecretCommand("/run/nixfleet-secrets/api-key")

	if !strings.Contains(cmd, "cat > '/run/nixfleet-secrets/api-key'") {
		t.Errorf("want a stdin read into the quoted dest, got: %s", cmd)
	}
	// Without this, the file exists at the umask default until the chmod lands.
	if !strings.HasPrefix(cmd, "umask 077 && ") {
		t.Errorf("want a restrictive umask before the write, got: %s", cmd)
	}
	if strings.Contains(cmd, "base64") || strings.Contains(cmd, "echo") {
		t.Errorf("value must not be piped in from the command line: %s", cmd)
	}
}

// A destination path containing a quote must not break out of its shell word.
func TestWriteSecretCommandQuotesDest(t *testing.T) {
	cmd := writeSecretCommand("/run/x'; id; '")
	if !strings.Contains(cmd, `'/run/x'\''; id; '\'''`) {
		t.Errorf("dest not escaped: %s", cmd)
	}
}

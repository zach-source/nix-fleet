package pullmode

import (
	"strings"
	"testing"
)

// The pull script embeds WEBHOOK_SECRET. It is streamed over stdin, so neither
// the script nor the secret may appear in the remote command (argv is readable
// by every user on the host via /proc).
func TestWriteRemoteFileCommandCarriesNoPayload(t *testing.T) {
	const marker = "s3cr3t-webhook-value"

	cfg := DefaultConfig()
	cfg.WebhookURL = "https://example.invalid/hook"
	cfg.WebhookSecret = marker

	script, err := renderPullScript(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, marker) {
		t.Fatalf("test is vacuous: rendered script does not contain the secret")
	}

	cmd := writeRemoteFileCommand("/usr/local/bin/nixfleet-pull", "0755")
	if strings.Contains(cmd, marker) {
		t.Errorf("remote command leaks the webhook secret: %s", cmd)
	}
	if strings.Contains(cmd, "WEBHOOK_SECRET") {
		t.Errorf("remote command embeds the script: %s", cmd)
	}
	if !strings.Contains(cmd, "cat > '/usr/local/bin/nixfleet-pull'") {
		t.Errorf("remote command should read the payload from stdin, got: %s", cmd)
	}
}

// A dest or mode containing a quote must not break out of its shell word.
func TestWriteRemoteFileCommandQuotesArgs(t *testing.T) {
	cmd := writeRemoteFileCommand("/tmp/x'; id; '", "0644")
	if strings.Contains(cmd, "; id; ") && !strings.Contains(cmd, `'\''; id; '\''`) {
		t.Errorf("dest not escaped: %s", cmd)
	}
	if !strings.HasPrefix(cmd, "umask 077 && ") {
		t.Errorf("want a restrictive umask before the write, got: %s", cmd)
	}
}

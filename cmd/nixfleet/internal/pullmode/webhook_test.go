package pullmode

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebhookSignatureRuntimeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhook")
	key := []byte("fixture-key'$(no-command)\n")
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatal(err)
	}
	payload := "{\"status\":\"ok\"}"
	got, err := WebhookSignature(path, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	if got != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("wrong HMAC")
	}
	if _, err := WebhookSignature(path+"-missing", strings.NewReader(payload)); err == nil {
		t.Fatal("missing key accepted")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WebhookSignature(path, strings.NewReader(payload)); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestPullScriptRuntimeSecret(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WebhookURL = "https://example.invalid/hook"
	cfg.WebhookSecretFile = "/run/private/webhook'$(no-command)"
	script, err := renderPullScript(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "WEBHOOK_SECRET=") || strings.Contains(script, "-hmac") {
		t.Fatal("runtime secret must not be embedded or expanded into argv")
	}
	if !strings.Contains(script, "webhook-signature --secret-file '/run/private/webhook'\\''$(no-command)'") {
		t.Fatal("runtime path was not shell-quoted")
	}
	cfg.WebhookSecret = "legacy-secret"
	if _, err := renderPullScript(cfg); err == nil {
		t.Fatal("ambiguous inline and file secrets accepted")
	}
	cfg.WebhookSecret = ""
	cfg.WebhookSecretFile = "relative/path"
	if _, err := renderPullScript(cfg); err == nil {
		t.Fatal("relative runtime path accepted")
	}
}

func TestRuntimeWebhookNotifyFailsClosed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WebhookURL = "https://example.invalid/hook"
	cfg.WebhookSecretFile = "/run/private/webhook"
	script, err := renderPullScript(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(script, "notify() {")
	end := strings.Index(script[start:], "\ncleanup()") + start
	// Run only notify with mocked commands; never execute the pull/deploy body.
	snippet := "set -euo pipefail\nHOST_NAME=test\nWEBHOOK_URL=https://example.invalid/hook\n" +
		"nixfleet() { return 7; }\ncurl() { echo unexpected-curl >&2; }\n" + script[start:end] + "\nnotify apply fixture\n"
	out, err := exec.Command("bash", "-c", snippet).CombinedOutput()
	if err == nil {
		t.Fatal("signer failure ignored")
	}
	if strings.Contains(string(out), "unexpected-curl") {
		t.Fatal("unsigned webhook sent after signer failure")
	}
}

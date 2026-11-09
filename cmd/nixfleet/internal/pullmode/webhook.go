package pullmode

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// WebhookSignature reads the key at runtime without putting it in process arguments.
// Key bytes are used verbatim, including any trailing newline.
func WebhookSignature(path string, payload io.Reader) (string, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading webhook secret file: %w", err)
	}
	if len(key) == 0 {
		return "", fmt.Errorf("webhook secret file is empty")
	}
	mac := hmac.New(sha256.New, key)
	if _, err := io.Copy(mac, payload); err != nil {
		return "", fmt.Errorf("reading webhook payload: %w", err)
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

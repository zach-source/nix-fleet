package juicefs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Field name constants for 1Password items. Keeping these in one place makes
// it possible to catch typos at compile time rather than at runtime when the
// CSI driver silently misses a key.
const (
	FieldAccessKey           = "access-key"
	FieldSecretKey           = "secret-key"
	FieldRootUser            = "root-user"
	FieldRootPassword        = "root-password"
	FieldUsername            = "username"
	FieldDatabase            = "database"
	FieldJuicefsUserPassword = "juicefs-user-password"
	FieldPassphrase          = "passphrase"
	FieldPrivateKey          = "private-key"
	FieldPublicKey           = "public-key"
	FieldFingerprint         = "fingerprint"
	FieldName                = "name"
	FieldMetaURL             = "metaurl"
	FieldStorage             = "storage"
	FieldBucket              = "bucket"
	FieldEnvs                = "envs"
	FieldEncryptRSAKey       = "encrypt_rsa_key"
)

type opItemRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ItemExists reports whether a 1Password item with the given title exists in
// the vault. Distinguishes "item not found" (returns false, nil) from other
// failures like expired auth or missing op binary (returns false, error).
func ItemExists(ctx context.Context, vault, title string) (bool, error) {
	out, err := runCombined(ctx, "op", "item", "get", title,
		"--vault", vault,
		"--format", "json")
	if err == nil {
		var ref opItemRef
		if err := json.Unmarshal(out, &ref); err != nil {
			return false, fmt.Errorf("parse op item get output: %w", err)
		}
		return ref.Title == title, nil
	}
	// op prints "<title>" isn't an item to stderr when the item is missing.
	// Any other error (auth, network, missing CLI) gets propagated.
	if bytes.Contains(out, []byte("isn't an item")) {
		return false, nil
	}
	return false, fmt.Errorf("op item get %q: %w: %s", title, err, strings.TrimSpace(string(out)))
}

// ItemCreate creates a password-category item. Fields use op CLI syntax:
// "label[type]=value" (types: text, password, concealed, file, ...).
func ItemCreate(ctx context.Context, vault, title string, fields []string) error {
	args := []string{
		"item", "create",
		"--category", "password",
		"--vault", vault,
		"--title", title,
	}
	args = append(args, fields...)
	out, err := runCombined(ctx, "op", args...)
	if err != nil {
		return fmt.Errorf("op item create %q: %w: %s", title, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ItemRead reads a single field via the op:// reference syntax. Uses
// runCombined so op's stderr is folded into the error when it fails —
// the CLI's failures are mostly informational ("not currently signed in",
// "item not found", etc.) and unhelpful without them.
func ItemRead(ctx context.Context, ref string) (string, error) {
	out, err := runCombined(ctx, "op", "read", ref)
	if err != nil {
		return "", fmt.Errorf("op read %q: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// OpReference builds an op:// URI.
func OpReference(vault, item, field string) string {
	return fmt.Sprintf("op://%s/%s/%s", vault, item, field)
}

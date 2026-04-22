package juicefs

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestEnsurePGItem_Idempotent verifies the "item exists → skip create"
// behavior without ever invoking openssl or the real op CLI.
func TestEnsurePGItem_Idempotent(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}.WithDefaults()

	t.Run("skips create when item exists", func(t *testing.T) {
		fr, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get juicefs-postgres": {
				Stdout: []byte(`{"id":"x","title":"juicefs-postgres"}`),
			},
		})
		withRunners(t, fn, fn)

		var out bytes.Buffer
		if err := EnsurePGItem(ctx, cfg, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := fr.Matches("op item create"); len(got) != 0 {
			t.Errorf("expected no create call, got %d", len(got))
		}
		if !strings.Contains(out.String(), "already present") {
			t.Errorf("expected skip message, got: %s", out.String())
		}
	})

	t.Run("creates item when missing", func(t *testing.T) {
		fr, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get juicefs-postgres": {
				Stdout: []byte(`[ERROR] isn't an item.`),
				Err:    errCalled("op", "exit 1"),
			},
			"op item create": {
				Stdout: []byte(`{"id":"new"}`),
			},
		})
		withRunners(t, fn, fn)

		if err := EnsurePGItem(ctx, cfg, bytes.NewBuffer(nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		creates := fr.Matches("op item create")
		if len(creates) != 1 {
			t.Fatalf("expected 1 create call, got %d", len(creates))
		}
		argv := creates[0].Argv()
		if !strings.Contains(argv, "--title juicefs-postgres") {
			t.Errorf("wrong title in argv: %s", argv)
		}
		if !strings.Contains(argv, "username[text]=juicefs") {
			t.Errorf("missing username field: %s", argv)
		}
		if !strings.Contains(argv, "database[text]=juicefs") {
			t.Errorf("missing database field: %s", argv)
		}
		if !strings.Contains(argv, "juicefs-user-password[password]=") {
			t.Errorf("missing password field: %s", argv)
		}
	})
}

// TestEnsureMinIOItem_Idempotent mirrors TestEnsurePGItem_Idempotent.
func TestEnsureMinIOItem_Idempotent(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}.WithDefaults()

	t.Run("skips when exists", func(t *testing.T) {
		fr, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get juicefs-minio": {
				Stdout: []byte(`{"id":"x","title":"juicefs-minio"}`),
			},
		})
		withRunners(t, fn, fn)

		if err := EnsureMinIOItem(ctx, cfg, bytes.NewBuffer(nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := fr.Matches("op item create"); len(got) != 0 {
			t.Errorf("expected no create, got %d", len(got))
		}
	})

	t.Run("creates with access-key + secret-key fields", func(t *testing.T) {
		fr, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get juicefs-minio": {
				Stdout: []byte(`[ERROR] isn't an item.`),
				Err:    errCalled("op", "exit 1"),
			},
			"op item create": {
				Stdout: []byte(`{"id":"new"}`),
			},
		})
		withRunners(t, fn, fn)

		if err := EnsureMinIOItem(ctx, cfg, bytes.NewBuffer(nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		creates := fr.Matches("op item create")
		if len(creates) != 1 {
			t.Fatalf("expected 1 create, got %d", len(creates))
		}
		argv := creates[0].Argv()
		for _, must := range []string{
			"--title juicefs-minio",
			"root-user[text]=jfsadmin",
			"root-password[password]=",
			"access-key[text]=juicefs-fleet",
			"secret-key[password]=",
		} {
			if !strings.Contains(argv, must) {
				t.Errorf("argv missing %q: %s", must, argv)
			}
		}
	})
}

// TestEnsureEncryptionKey_Idempotent_Skip verifies the fast-path only (no
// keygen invoked). The full-keygen path is exercised in integration tests
// since it shells out to openssl.
func TestEnsureEncryptionKey_Idempotent_Skip(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}.WithDefaults()

	fr, fn := newFakeRunner(t, map[string]fakeResponse{
		"op item get juicefs-encryption-key": {
			Stdout: []byte(`{"id":"x","title":"juicefs-encryption-key"}`),
		},
	})
	withRunners(t, fn, fn)

	var out bytes.Buffer
	if err := EnsureEncryptionKey(ctx, cfg, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := fr.Matches("op item create"); len(got) != 0 {
		t.Errorf("expected no create call, got %d", len(got))
	}
	if !strings.Contains(out.String(), "already present") {
		t.Errorf("expected skip message, got: %s", out.String())
	}
}

// TestEnsureCSISecret_URLComposition is the highest-value bootstrap test:
// the composite item's URLs must hit the right cluster-internal names.
func TestEnsureCSISecret_URLComposition(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}.WithDefaults()

	readMap := map[string]string{
		"op read op://Personal Agents/juicefs-postgres/juicefs-user-password": "pg-pw-123",
		"op read op://Personal Agents/juicefs-minio/access-key":               "ak-xyz",
		"op read op://Personal Agents/juicefs-minio/secret-key":               "sk-abc",
		"op read op://Personal Agents/juicefs-encryption-key/passphrase":      "my-passphrase",
		"op read op://Personal Agents/juicefs-encryption-key/private-key":     "-----BEGIN ENCRYPTED PRIVATE KEY-----\nPEM_CONTENT\n-----END ENCRYPTED PRIVATE KEY-----",
	}
	responses := map[string]fakeResponse{
		"op item get juicefs-csi-secret": {
			Stdout: []byte(`[ERROR] isn't an item.`),
			Err:    errCalled("op", "exit 1"),
		},
		"op item create": {
			Stdout: []byte(`{"id":"new"}`),
		},
	}
	for k, v := range readMap {
		responses[k] = fakeResponse{Stdout: []byte(v + "\n")}
	}

	fr, fn := newFakeRunner(t, responses)
	withRunners(t, fn, fn)

	if err := EnsureCSISecret(ctx, cfg, bytes.NewBuffer(nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	creates := fr.Matches("op item create")
	if len(creates) != 1 {
		t.Fatalf("expected 1 create, got %d", len(creates))
	}
	argv := creates[0].Argv()

	// Required composite fields.
	wantFields := []string{
		"name[text]=fleet",
		"storage[text]=s3",
		"access-key[text]=ak-xyz",
		"secret-key[password]=sk-abc",
		`envs[text]={"JFS_RSA_PASSPHRASE":"my-passphrase"}`,
	}
	for _, want := range wantFields {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q", want)
		}
	}

	// URL composition — these must be exact to match cluster service DNS.
	wantMeta := "metaurl[password]=postgres://juicefs:pg-pw-123@juicefs-postgres.juicefs-system.svc.cluster.local:5432/juicefs?sslmode=disable"
	if !strings.Contains(argv, wantMeta) {
		t.Errorf("metaurl not composed correctly.\nwant substring: %s\nfull argv:  %s", wantMeta, argv)
	}
	wantBucket := "bucket[text]=http://juicefs-minio.juicefs-system.svc.cluster.local:9000/fleet"
	if !strings.Contains(argv, wantBucket) {
		t.Errorf("bucket not composed correctly.\nwant substring: %s\nfull argv:  %s", wantBucket, argv)
	}

	// RSA key inlined verbatim.
	if !strings.Contains(argv, "BEGIN ENCRYPTED PRIVATE KEY") {
		t.Errorf("encrypt_rsa_key field missing PEM content: %s", argv)
	}
}

// errCalled constructs a minimal non-nil error that mimics a CLI failure.
// We don't use a typed error because op CLI errors come back as *exec.ExitError
// in production and that's not worth faking precisely.
type cliErr struct{ msg string }

func (e cliErr) Error() string { return e.msg }

func errCalled(cmd, reason string) error {
	return cliErr{msg: cmd + ": " + reason}
}

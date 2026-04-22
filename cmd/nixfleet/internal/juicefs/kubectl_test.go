package juicefs

import (
	"context"
	"encoding/base64"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetSecretField(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}.WithDefaults()

	t.Run("base64-decodes kubectl output", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte("s3cret"))
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"kubectl -n juicefs-system get secret pg": {
				Stdout: []byte(encoded),
			},
		})
		withRunners(t, fn, nil)

		got, err := cfg.GetSecretField(ctx, "pg", "password")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "s3cret" {
			t.Errorf("got %q, want %q", got, "s3cret")
		}
	})

	t.Run("returns empty string when kubectl errors (not ready)", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"kubectl -n juicefs-system get secret missing": {
				Err: errors.New("NotFound"),
			},
		})
		withRunners(t, fn, nil)

		got, err := cfg.GetSecretField(ctx, "missing", "any")
		if err != nil {
			t.Fatalf("expected nil error on not-ready, got: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("returns empty string when field is empty", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"kubectl -n juicefs-system get secret pg": {
				Stdout: []byte(""),
			},
		})
		withRunners(t, fn, nil)

		got, _ := cfg.GetSecretField(ctx, "pg", "missing-field")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("returns error on invalid base64", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"kubectl -n juicefs-system get secret pg": {
				Stdout: []byte("!!!not-base64!!!"),
			},
		})
		withRunners(t, fn, nil)

		_, err := cfg.GetSecretField(ctx, "pg", "x")
		if err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestWaitForSecret(t *testing.T) {
	cfg := Config{}.WithDefaults()

	t.Run("returns when all fields populated", func(t *testing.T) {
		// On call N>=2, return populated values. Earlier calls return empty.
		var calls int32
		encoded := base64.StdEncoding.EncodeToString([]byte("v"))
		fn := execFn(func(ctx context.Context, name string, args ...string) ([]byte, error) {
			n := atomic.AddInt32(&calls, 1)
			if n >= 3 {
				return []byte(encoded), nil
			}
			return []byte(""), nil
		})
		withRunners(t, fn, nil)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := cfg.WaitForSecret(ctx, "any", []string{"a", "b"}, 10*time.Millisecond)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("times out when fields never populate", func(t *testing.T) {
		fn := execFn(func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(""), nil
		})
		withRunners(t, fn, nil)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := cfg.WaitForSecret(ctx, "any", []string{"a"}, 20*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error")
		}
	})
}

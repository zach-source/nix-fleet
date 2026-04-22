package juicefs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestItemExists(t *testing.T) {
	ctx := context.Background()

	t.Run("returns true when title matches", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get target": {
				Stdout: []byte(`{"id":"abc","title":"target"}`),
			},
		})
		withRunners(t, nil, fn)

		got, err := ItemExists(ctx, "vault", "target")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Error("got false, want true")
		}
	})

	t.Run("returns false when op says 'isn't an item'", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get missing": {
				Stdout: []byte(`[ERROR] "missing" isn't an item. Specify the item with its UUID, name, or domain.`),
				Err:    errors.New("exit status 1"),
			},
		})
		withRunners(t, nil, fn)

		got, err := ItemExists(ctx, "vault", "missing")
		if err != nil {
			t.Fatalf("expected nil error on missing item, got: %v", err)
		}
		if got {
			t.Error("got true, want false")
		}
	})

	t.Run("propagates real errors (e.g. auth expired)", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get x": {
				Stdout: []byte("[ERROR] you are not currently signed in. Please run `op signin`."),
				Err:    errors.New("exit status 1"),
			},
		})
		withRunners(t, nil, fn)

		_, err := ItemExists(ctx, "vault", "x")
		if err == nil {
			t.Fatal("expected error for auth failure, got nil")
		}
	})

	t.Run("returns error on invalid JSON", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item get garbled": {
				Stdout: []byte(`{not json`),
			},
		})
		withRunners(t, nil, fn)

		_, err := ItemExists(ctx, "vault", "garbled")
		if err == nil {
			t.Fatal("expected parse error, got nil")
		}
	})
}

func TestItemCreate(t *testing.T) {
	ctx := context.Background()

	t.Run("builds correct argv and succeeds", func(t *testing.T) {
		fr, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item create": {Stdout: []byte(`{"id":"xyz"}`)},
		})
		withRunners(t, nil, fn)

		err := ItemCreate(ctx, "Personal Agents", "my-item", []string{
			"username[text]=alice",
			"password[password]=hunter2",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		calls := fr.Matches("op item create")
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		args := calls[0].Args
		// Check required flags present.
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--vault Personal Agents") {
			t.Errorf("argv missing --vault: %s", joined)
		}
		if !strings.Contains(joined, "--title my-item") {
			t.Errorf("argv missing --title: %s", joined)
		}
		if !strings.Contains(joined, "--category password") {
			t.Errorf("argv missing --category: %s", joined)
		}
		if !strings.Contains(joined, "username[text]=alice") {
			t.Errorf("argv missing field: %s", joined)
		}
	})

	t.Run("returns error with stderr context on failure", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"op item create": {
				Stdout: []byte("op: vault not found\n"),
				Err:    errors.New("exit status 1"),
			},
		})
		withRunners(t, nil, fn)

		err := ItemCreate(ctx, "bogus", "x", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "vault not found") {
			t.Errorf("error missing stderr context: %v", err)
		}
	})
}

func TestItemRead(t *testing.T) {
	ctx := context.Background()

	t.Run("trims trailing newline", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"op read op://v/i/f": {Stdout: []byte("secret-value\n")},
		})
		withRunners(t, fn, nil)

		got, err := ItemRead(ctx, "op://v/i/f")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "secret-value" {
			t.Errorf("got %q, want %q", got, "secret-value")
		}
	})

	t.Run("propagates error", func(t *testing.T) {
		_, fn := newFakeRunner(t, map[string]fakeResponse{
			"op read op://v/missing/f": {Err: errors.New("exit 1")},
		})
		withRunners(t, fn, nil)

		_, err := ItemRead(ctx, "op://v/missing/f")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaleObservationCannotOverwriteDeployment(t *testing.T) {
	f := &localStateExec{dir: t.TempDir()}
	m := NewManager()
	ctx := context.Background()
	observation, err := m.ReadState(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := m.ReadState(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	deployment.CurrentGeneration = 42
	if err := m.WriteState(ctx, f, deployment); err != nil {
		t.Fatal(err)
	}
	observation.RebootRequired = true
	if err := m.WriteState(ctx, f, observation); err == nil {
		t.Fatal("stale observation silently overwrote deployment")
	}
	got, err := m.ReadState(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentGeneration != 42 {
		t.Fatalf("lost generation: %d", got.CurrentGeneration)
	}
}
func TestStatePreservesUnknownAdditiveFields(t *testing.T) {
	f := &localStateExec{dir: t.TempDir()}
	path := filepath.Join(f.dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"state_version":1,"future_field":{"value":42}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	if err := m.UpdateRebootStatus(context.Background(), f, false, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "future_field") {
		t.Fatal("unknown additive field lost")
	}
}

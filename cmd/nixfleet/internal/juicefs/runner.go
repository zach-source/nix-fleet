package juicefs

import (
	"context"
	"os/exec"
)

// execFn is the shape of a single external-command invocation. Tests swap
// runOutput / runCombined to stub out op/openssl/kubectl/juicefs.
type execFn func(ctx context.Context, name string, args ...string) ([]byte, error)

// runOutput runs `name args...` and returns stdout only (error on nonzero
// exit, matching exec.Cmd.Output() semantics).
var runOutput execFn = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// runCombined runs `name args...` and returns combined stdout+stderr
// (error on nonzero exit, matching exec.Cmd.CombinedOutput() semantics).
// Use when stderr carries useful failure detail.
var runCombined execFn = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

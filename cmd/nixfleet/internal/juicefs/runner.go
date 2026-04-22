package juicefs

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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

// runPipe runs `name args...` with the given bytes written to stdin. Returns
// non-nil error with combined output context on nonzero exit. The stdin
// path is load-bearing for openssl: passphrase transport via stdin avoids
// env-var and shell-quoting edge cases that `pass:` and `env:` hit.
var runPipe = func(ctx context.Context, stdin []byte, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

package juicefs

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// recordedCall captures a single external-command invocation for assertions.
type recordedCall struct {
	Name string
	Args []string
}

// Argv returns "name arg1 arg2 ..." for matching.
func (r recordedCall) Argv() string {
	return strings.Join(append([]string{r.Name}, r.Args...), " ")
}

// fakeRunner is a test double for execFn. Responses is keyed by prefix-match
// of "name arg1 arg2" (enough args to uniquely identify the call). The first
// matching prefix wins, so put more-specific keys before less-specific ones
// — or supply distinct prefixes.
type fakeRunner struct {
	t         *testing.T
	responses map[string]fakeResponse
	calls     []recordedCall
}

type fakeResponse struct {
	Stdout []byte
	Err    error
}

// newFakeRunner returns a fake along with its execFn closure. Use Matches to
// list what was called.
func newFakeRunner(t *testing.T, responses map[string]fakeResponse) (*fakeRunner, execFn) {
	t.Helper()
	fr := &fakeRunner{t: t, responses: responses}
	fn := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		call := recordedCall{Name: name, Args: args}
		fr.calls = append(fr.calls, call)
		argv := call.Argv()
		for prefix, resp := range responses {
			if strings.HasPrefix(argv, prefix) {
				return resp.Stdout, resp.Err
			}
		}
		t.Fatalf("unexpected command: %s", argv)
		return nil, fmt.Errorf("unexpected call: %s", argv)
	}
	return fr, fn
}

// Matches returns calls whose argv starts with prefix.
func (f *fakeRunner) Matches(prefix string) []recordedCall {
	var out []recordedCall
	for _, c := range f.calls {
		if strings.HasPrefix(c.Argv(), prefix) {
			out = append(out, c)
		}
	}
	return out
}

// withRunners swaps runOutput + runCombined for the test duration.
// Pass nil for either to keep the original. Restores on t.Cleanup.
func withRunners(t *testing.T, output, combined execFn) {
	t.Helper()
	origOutput := runOutput
	origCombined := runCombined
	if output != nil {
		runOutput = output
	}
	if combined != nil {
		runCombined = combined
	}
	t.Cleanup(func() {
		runOutput = origOutput
		runCombined = origCombined
	})
}

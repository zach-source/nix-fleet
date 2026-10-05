package ssh

import (
	"os/exec"
	"testing"
)

func TestShellQuoteRoundTrip(t *testing.T) {
	for _, in := range []string{"", "plain", "it's", "a b", "$(id) `id` $HOME", "x; rm -rf /", "line1\nline2", `'"\`} {
		out, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(in)).Output()
		if err != nil {
			t.Fatalf("sh failed for %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("ShellQuote(%q) round-tripped to %q", in, out)
		}
	}
}

func TestSudoCommandWrapsWholeCommand(t *testing.T) {
	got := sudoCommand("mkdir -p /x && echo 'hi' > /x/f")
	want := `sudo sh -c 'mkdir -p /x && echo '\''hi'\'' > /x/f'`
	if got != want {
		t.Errorf("sudoCommand = %s, want %s", got, want)
	}
}

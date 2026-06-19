package osupdate

import "testing"

func TestShQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "'plain'"},
		{"with space", "'with space'"},
		{"/var/log/nixfleet", "'/var/log/nixfleet'"},
		// A value containing a single quote must be split-and-escaped so it can't
		// break out of the surrounding quotes (shell-injection safety).
		{"it's", `'it'\''s'`},
		{"a'; rm -rf /; '", `'a'\''; rm -rf /; '\'''`},
	}
	for _, c := range cases {
		if got := shQuote(c.in); got != c.want {
			t.Errorf("shQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultReleaseUpgradeConfig(t *testing.T) {
	c := DefaultReleaseUpgradeConfig()
	if c.MinFreeRootMB <= 0 {
		t.Errorf("expected positive MinFreeRootMB, got %d", c.MinFreeRootMB)
	}
	if c.LogPath == "" || c.Unit == "" {
		t.Errorf("expected non-empty LogPath and Unit, got %q / %q", c.LogPath, c.Unit)
	}
}

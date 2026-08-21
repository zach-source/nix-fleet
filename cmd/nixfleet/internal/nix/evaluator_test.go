package nix

import "testing"

// Both renderings are in the fleet: object-keyed on recent Nix, array on older.
func TestParseNarHash(t *testing.T) {
	const path = "/nix/store/abc-nixfleet-ubuntu-system-gtr-150"
	const want = "sha256-4vkFxB32zAiIAPNWE52di2L2/S9xiJokWCaewSH7YWw="

	cases := map[string]struct {
		json string
		want string
	}{
		"object": {`{"` + path + `":{"narHash":"` + want + `"}}`, want},
		"array":  {`[{"path":"` + path + `","narHash":"` + want + `"}]`, want},
		"empty":  {`{}`, ""},
		"junk":   {`not json`, ""},
	}

	for name, tc := range cases {
		if got := parseNarHash([]byte(tc.json), path); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

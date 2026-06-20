package synology

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"500G", 500 << 30, false},
		{"1T", 1 << 40, false},
		{"512M", 512 << 20, false},
		{"10737418240", 10737418240, false},
		{"2GB", 2 << 30, false}, // GB suffix tolerated
		{"1.5G", int64(1.5 * float64(1<<30)), false},
		{"", 0, true},
		{"banana", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSize(%q): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSize(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDSMCreateType(t *testing.T) {
	cases := map[string]string{"THIN": "BLUN", "": "BLUN", "FILE": "FILE", "ADV": "ADV"}
	for in, want := range cases {
		if got := dsmCreateType(LUN{Type: in}); got != want {
			t.Errorf("dsmCreateType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNFSRuleToDSM(t *testing.T) {
	r := NFSRule{Client: "192.168.3.0/24", Access: "ro", Squash: "all_squash", Secure: true, Async: false}
	d := r.toDSM()
	if d.Client != "192.168.3.0/24" || d.Privilege != "ro" || d.RootSquash != "all" {
		t.Errorf("mapping mismatch: %+v", d)
	}
	if d.Insecure { // secure=true => insecure=false
		t.Errorf("secure=true should map to insecure=false, got insecure=true")
	}
	if !d.Security.Sys || !d.Crossmnt {
		t.Errorf("expected sys+crossmnt defaults, got %+v", d)
	}

	// defaults: empty access -> rw, empty squash -> root, secure default false -> insecure
	def := NFSRule{Client: "*"}.toDSM()
	if def.Privilege != "rw" || def.RootSquash != "root" || !def.Insecure {
		t.Errorf("default mapping wrong: %+v", def)
	}
}

func TestNFSRulesMatch(t *testing.T) {
	declared := []NFSRule{{Client: "*", Access: "rw", Squash: "root_squash", Secure: false, Async: true}}
	// actual that equals the declared rule (the live k0s-gti shape)
	actual := []DSMNFSRule{{Client: "*", Privilege: "rw", RootSquash: "root", Async: true, Insecure: true, Crossmnt: true}}
	actual[0].Security.Sys = true
	if !NFSRulesMatch(declared, actual) {
		t.Errorf("expected match for equivalent rule sets")
	}

	// drift: different client
	drift := []DSMNFSRule{{Client: "10.0.0.0/8", Privilege: "rw", RootSquash: "root", Async: true, Insecure: true, Crossmnt: true}}
	drift[0].Security.Sys = true
	if NFSRulesMatch(declared, drift) {
		t.Errorf("expected mismatch on differing client")
	}

	// drift: different count
	if NFSRulesMatch(declared, nil) {
		t.Errorf("expected mismatch on empty actual")
	}
}

func TestSettingValues(t *testing.T) {
	s := Setting{API: "SYNO.Core.FileServ.NFS", Method: "set", Params: map[string]string{"enable_nfs": "true", "read_size": "8192"}}
	if s.version() != 1 {
		t.Errorf("default version should be 1, got %d", s.version())
	}
	v := s.values()
	if v.Get("enable_nfs") != "true" || v.Get("read_size") != "8192" {
		t.Errorf("values mismatch: %v", v)
	}
	// String must not leak param values (keys only)
	if got := s.String(); got == "" || containsValue(got, "8192") {
		t.Errorf("String() should list keys without values, got %q", got)
	}
}

func containsValue(s, v string) bool {
	for i := 0; i+len(v) <= len(s); i++ {
		if s[i:i+len(v)] == v {
			return true
		}
	}
	return false
}

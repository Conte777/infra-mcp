package buildinfo

import "testing"

// The fallback is the whole point: callers print this string, and a build with
// no recorded version must still say something.
func TestVersionFallsBackWhenTheBuildRecordsNothing(t *testing.T) {
	if got := Version(); got == "" {
		t.Fatal("Version() = \"\", want a non-empty fallback")
	}
}

func TestIsRelease(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"v1.2.3", true},
		{"v1.2.3-rc1", true},
		{"v0.0.0-20260821164229-5102f47ba11b", false},
		{"v1.2.4-0.20260821164229-5102f47ba11b", false},
		{"v0.0.0-20260821164229-5102f47ba11b+dirty", false},
		{"v1.2.3+dirty", false},
		{"(devel)", false},
	} {
		if got := isRelease(tc.v); got != tc.want {
			t.Errorf("isRelease(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

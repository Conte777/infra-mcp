package buildinfo

import "testing"

func TestVersionPrefersStampedValue(t *testing.T) {
	t.Cleanup(func() { version = "" })
	version = "v1.2.3"

	if got := Version(); got != "v1.2.3" {
		t.Fatalf("Version() = %q, want %q", got, "v1.2.3")
	}
}

func TestVersionFallsBackWhenUnstamped(t *testing.T) {
	if got := Version(); got == "" {
		t.Fatal("Version() = \"\", want a non-empty fallback")
	}
}

func TestIsRelease(t *testing.T) {
	t.Cleanup(func() { version = "" })

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
		version = tc.v
		if got := IsRelease(); got != tc.want {
			t.Errorf("IsRelease() = %v for %q, want %v", got, tc.v, tc.want)
		}
	}
}

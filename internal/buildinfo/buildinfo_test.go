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

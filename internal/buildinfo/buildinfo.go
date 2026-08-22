// Package buildinfo reports the version of the running binary.
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// version is stamped at release time via -ldflags; empty in a plain `go build`.
var version string

// Version returns the stamped version, falling back to the module version the
// go toolchain records, and finally to "(devel)" for an unstamped local build.
func Version() string {
	if version != "" {
		return version
	}

	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}

	return "(devel)"
}

// pseudoVersion is what the go toolchain synthesises for a build from a clone:
// a timestamp and a commit hash, with no tag behind them. The separator before
// the timestamp is "." when a tag preceded the commit and "-" when none did.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}$`)

// IsRelease reports whether [Version] names a tag that exists in the repository.
// A pseudo-version does not, so a URL built from one resolves to nothing.
func IsRelease() bool {
	v := Version()
	// "+dirty" is stamped when the tree has uncommitted changes: no such ref,
	// and the config types may no longer match the tag's schema anyway.
	return strings.HasPrefix(v, "v") &&
		!strings.Contains(v, "+") &&
		!pseudoVersion.MatchString(v)
}

// Package buildinfo reports the version of the running binary.
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// Version returns the module version the go toolchain records, falling back to
// "(devel)" for a build from a clone. The plugin installs through
// `go install …@<tag>`, so a released binary carries its tag here.
func Version() string {
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
func IsRelease() bool { return isRelease(Version()) }

func isRelease(v string) bool {
	// "+dirty" is stamped when the tree has uncommitted changes: no such ref,
	// and the config types may no longer match the tag's schema anyway.
	return strings.HasPrefix(v, "v") &&
		!strings.Contains(v, "+") &&
		!pseudoVersion.MatchString(v)
}

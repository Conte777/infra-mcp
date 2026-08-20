// Package buildinfo reports the version of the running binary.
package buildinfo

import "runtime/debug"

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

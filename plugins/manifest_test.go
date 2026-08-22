// Package plugins carries no code: a plugin is a manifest, an .mcp.json and a
// shell launcher. These tests are the only gate those three files have, and
// they ride in `task test` rather than a CI target of their own.
package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// globPlugins walks the plugin tree rather than naming postgres: the five
// remaining sources are clones of it, and a test that names one covers one.
func globPlugins(t *testing.T, pattern, what string) []string {
	t.Helper()

	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no plugin %s found next to this test", what)
	}
	return paths
}

func manifests(t *testing.T) []string {
	t.Helper()

	return globPlugins(t, "*/.claude-plugin/plugin.json", "manifest")
}

func manifestVersion(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m.Version
}

// The tag is one per repository — cmd/ lives in the root module, so
// `go install …@vX.Y.Z` resolves a tag on the root — while manifests are one
// per plugin. A manifest that disagrees names a tag nobody pushed, and its
// launcher fails at first start on the machine that installed the plugin.
func TestManifestVersionsAgree(t *testing.T) {
	byVersion := map[string][]string{}
	for _, path := range manifests(t) {
		v := manifestVersion(t, path)
		byVersion[v] = append(byVersion[v], path)
	}

	if len(byVersion) > 1 {
		t.Errorf("plugin manifests carry %d different versions, want one; run `task version:set -- X.Y.Z`", len(byVersion))
		for v, paths := range byVersion {
			t.Errorf("  %q: %v", v, paths)
		}
	}
}

// The launcher builds "v" + this string, and the release workflow tags it, so
// the "v" belongs to neither.
var tagVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

func TestManifestVersionIsATagWithoutThePrefix(t *testing.T) {
	for _, path := range manifests(t) {
		if v := manifestVersion(t, path); !tagVersion.MatchString(v) {
			t.Errorf("%s: version = %q, want X.Y.Z with no leading v", path, v)
		}
	}
}

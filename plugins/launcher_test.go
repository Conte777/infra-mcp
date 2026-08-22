package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goStub stands in for the toolchain behind the launcher's ${INFRA_MCP_GO:-go}
// seam: it records its argv and produces the binary `go install` would have.
const goStub = `#!/bin/sh
set -eu
printf '%s\n' "$@" >> "$INFRA_MCP_TEST_ARGS"
if [ -n "${INFRA_MCP_TEST_FAIL:-}" ]; then
	echo "go: github.com/Conte777/infra-mcp/cmd/x@v9.9.9: unknown revision v9.9.9" >&2
	exit 1
fi
last=
for a in "$@"; do last=$a; done
pkg=${last%@*}
mkdir -p "$GOBIN"
printf '#!/bin/sh\necho "stub binary: $*"\n' > "$GOBIN/${pkg##*/}"
chmod +x "$GOBIN/${pkg##*/}"
`

type launcherEnv struct {
	t     *testing.T
	cache string
	stub  string
	argv  string
	fail  bool
}

func newLauncherEnv(t *testing.T) *launcherEnv {
	t.Helper()

	dir := t.TempDir()
	env := &launcherEnv{
		t:     t,
		cache: filepath.Join(dir, "cache"),
		stub:  filepath.Join(dir, "go"),
		argv:  filepath.Join(dir, "argv"),
	}
	if err := os.WriteFile(env.stub, []byte(goStub), 0o755); err != nil {
		t.Fatal(err)
	}
	return env
}

func (e *launcherEnv) run(script string, args ...string) (string, error) {
	e.t.Helper()

	cmd := exec.CommandContext(e.t.Context(), script, args...)
	cmd.Env = append(os.Environ(),
		"INFRA_MCP_GO="+e.stub,
		"XDG_CACHE_HOME="+e.cache,
		"INFRA_MCP_TEST_ARGS="+e.argv,
	)
	if e.fail {
		cmd.Env = append(cmd.Env, "INFRA_MCP_TEST_FAIL=1")
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// goArgs is what the stub was called with, empty when it never ran.
func (e *launcherEnv) goArgs() string {
	e.t.Helper()

	raw, err := os.ReadFile(e.argv)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

// launchers walks the plugin tree rather than naming postgres: the five
// remaining sources are clones of it, and a test that names one covers one.
func launchers(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob("*/bin/infra-mcp-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no plugin launcher found next to this test")
	}
	return paths
}

func manifestOf(t *testing.T, launcher string) string {
	t.Helper()

	return filepath.Join(filepath.Dir(filepath.Dir(launcher)), ".claude-plugin", "plugin.json")
}

func TestLauncherBuildsTheVersionFromTheManifest(t *testing.T) {
	for _, launcher := range launchers(t) {
		t.Run(launcher, func(t *testing.T) {
			name := filepath.Base(launcher)
			want := "github.com/Conte777/infra-mcp/cmd/" + name + "@v" + manifestVersion(t, manifestOf(t, launcher))

			env := newLauncherEnv(t)
			out, err := env.run("./"+launcher, "--version")
			if err != nil {
				t.Fatalf("launcher: %v\n%s", err, out)
			}

			if got := env.goArgs(); !strings.Contains(got, want) {
				t.Errorf("go install package = %q, want it to contain %q", got, want)
			}
			if !strings.Contains(out, "stub binary: --version") {
				t.Errorf("output = %q, want the built binary to have been exec'd with the arguments", out)
			}
		})
	}
}

func TestLauncherEvictsPreviousVersions(t *testing.T) {
	for _, launcher := range launchers(t) {
		t.Run(launcher, func(t *testing.T) {
			name := filepath.Base(launcher)
			version := "v" + manifestVersion(t, manifestOf(t, launcher))

			env := newLauncherEnv(t)
			stale := filepath.Join(env.cache, "infra-mcp", name, "v0.0.1")
			if err := os.MkdirAll(stale, 0o755); err != nil {
				t.Fatal(err)
			}

			if out, err := env.run("./" + launcher); err != nil {
				t.Fatalf("launcher: %v\n%s", err, out)
			}

			entries, err := os.ReadDir(filepath.Join(env.cache, "infra-mcp", name))
			if err != nil {
				t.Fatal(err)
			}
			var left []string
			for _, e := range entries {
				left = append(left, e.Name())
			}
			if len(left) != 1 || left[0] != version {
				t.Errorf("cache holds %v, want only %q", left, version)
			}
		})
	}
}

func TestLauncherWrapsABuildFailure(t *testing.T) {
	for _, launcher := range launchers(t) {
		t.Run(launcher, func(t *testing.T) {
			env := newLauncherEnv(t)
			env.fail = true

			out, err := env.run("./" + launcher)
			if err == nil {
				t.Fatalf("launcher exited 0 on a failed build\n%s", out)
			}
			if !strings.Contains(out, "unknown revision") {
				t.Errorf("output = %q, want the toolchain's own error kept", out)
			}
			if !strings.Contains(out, "not published yet") {
				t.Errorf("output = %q, want the unpublished-release hint", out)
			}
		})
	}
}

func TestLauncherRejectsAManifestWithoutAVersion(t *testing.T) {
	launcher := launchers(t)[0]
	script, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(`{"name": "infra-mcp-postgres"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(root, "bin", filepath.Base(launcher))
	if err := os.WriteFile(copied, script, 0o755); err != nil {
		t.Fatal(err)
	}

	env := newLauncherEnv(t)
	out, err := env.run(copied)
	if err == nil {
		t.Fatalf("launcher exited 0 on a manifest with no version\n%s", out)
	}
	if !strings.Contains(out, "no version in") {
		t.Errorf("output = %q, want it to name the missing version", out)
	}
	if got := env.goArgs(); got != "" {
		t.Errorf("go was called with %q, want no build attempt", got)
	}
}

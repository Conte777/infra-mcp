package mcpsrv

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testReadTools struct {
	Extra []string `json:"extra,omitzero"`
}

type testConnection struct {
	Host     string `json:"host,omitzero"`
	Password string `json:"password,omitzero" mcpsrv:"secret"`
}

// testCluster is a source's cluster type: the core's half embedded, and the
// source's own groups. postgres keeps its secret one plain group down, but the
// core has to reach one wherever a source can put it, so every container form
// is represented here.
type testCluster struct {
	ClusterCommon

	Connection testConnection            `json:"connection,omitzero"`
	Brokers    []testConnection          `json:"brokers,omitzero"`
	Shards     map[string]testConnection `json:"shards,omitzero"`
	Fallback   *testConnection           `json:"fallback,omitzero"`
}

// testChain reaches itself: a config type may, and the walk over it may not
// follow the cycle forever.
type testChain struct {
	Next       *testChain     `json:"next,omitzero"`
	Connection testConnection `json:"connection,omitzero"`
}

// testTree reaches itself without passing through a struct, so the guard that
// stops testChain never sees it.
type testTree map[string]testTree

type testGrove struct {
	Tree testTree `json:"tree,omitzero"`
}

// testBundle marks a container as the secret: what must be a ${VAR} is every
// string inside it, not the list holding them.
type testBundle struct {
	Passwords []string `json:"passwords,omitzero" mcpsrv:"secret"`
}

type testConfig struct {
	Common[testCluster, testReadTools]
	testCluster
}

// testEnvironments is the shortest config that declares a cluster.
func testEnvironments(cluster string) string {
	return `"environments":{"dev":{"clusters":{"main":` + cluster + `}}}`
}

func TestLocationResolvePrefersFlag(t *testing.T) {
	loc := Location{Source: "postgres", Flag: "/given/by/flag.json"}
	t.Setenv(loc.EnvVar(), "/from/env.json")

	path, searched, err := loc.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != loc.Flag {
		t.Errorf("path = %q, want %q", path, loc.Flag)
	}
	if len(searched) != 1 {
		t.Errorf("searched = %v, want only the flag", searched)
	}
}

// The rule itself, not the --config example of it: a path the operator named
// is returned as named even when the file is absent, and --init writes to that
// same path. Falling through would answer with another environment's database.
func TestLocationResolveKeepsAMissingNamedPath(t *testing.T) {
	const missing = "/nonexistent/nope.json"
	tests := []struct {
		name  string
		named func(t *testing.T, loc *Location)
		want  string
	}{
		{"flag", func(_ *testing.T, loc *Location) { loc.Flag = missing }, missing},
		{"env", func(t *testing.T, loc *Location) { t.Setenv(loc.EnvVar(), missing) }, "INFRA_MCP_POSTGRES_CONFIG=" + missing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			loc := Location{Source: "postgres"}
			writeFile(t, loc.XDGPath(), "{}") // the trap: a readable file one candidate down
			tt.named(t, &loc)

			path, searched, err := loc.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if path != missing {
				t.Errorf("path = %q, want the named %q", path, missing)
			}
			if len(searched) != 1 || searched[0] != tt.want {
				t.Errorf("searched = %v, want [%q]", searched, tt.want)
			}
			if got := loc.InitPath(); got != path {
				t.Errorf("InitPath = %q, but Resolve reads %q", got, path)
			}
		})
	}
}

func TestLocationResolveOrder(t *testing.T) {
	dir := t.TempDir()
	loc := Location{Source: "postgres"}

	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// A variable exported in the shell running the tests would win outright.
	t.Setenv(loc.EnvVar(), "")
	writeFile(t, filepath.Join(xdg, "infra-mcp", "postgres.json"), "{}")

	path, _, err := loc.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := loc.XDGPath(); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	env := filepath.Join(dir, "env.json")
	writeFile(t, env, "{}")
	t.Setenv(loc.EnvVar(), env)

	path, searched, err := loc.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != env {
		t.Errorf("path = %q, want the env one %q", path, env)
	}
	if len(searched) != 1 || !strings.Contains(searched[0], loc.EnvVar()) {
		t.Errorf("searched = %v, want the env var named", searched)
	}
}

func TestLocationResolveNothingFound(t *testing.T) {
	loc := Location{Source: "postgres"}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "empty"))
	t.Setenv(loc.EnvVar(), "")

	_, searched, err := loc.Resolve()
	if err == nil {
		t.Fatal("Resolve succeeded with no config anywhere")
	}
	if len(searched) == 0 {
		t.Error("searched is empty; the error cannot say where we looked")
	}
}

func TestLocationEnvVarIsPerSource(t *testing.T) {
	if got := (Location{Source: "postgres"}).EnvVar(); got != "INFRA_MCP_POSTGRES_CONFIG" {
		t.Errorf("EnvVar = %q", got)
	}
}

func TestExpandString(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_SET", "value")
	if err := os.Unsetenv("INFRA_MCP_TEST_UNSET"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "no reference", in: "plain", want: "plain"},
		{name: "set", in: "${INFRA_MCP_TEST_SET}", want: "value"},
		{name: "embedded", in: "a-${INFRA_MCP_TEST_SET}-b", want: "a-value-b"},
		{name: "default used", in: "${INFRA_MCP_TEST_UNSET:-fallback}", want: "fallback"},
		{name: "empty default used", in: "${INFRA_MCP_TEST_UNSET:-}", want: ""},
		{name: "default ignored when set", in: "${INFRA_MCP_TEST_SET:-fallback}", want: "value"},
		{name: "missing", in: "${INFRA_MCP_TEST_UNSET}", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandString(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expandString(%q) = %q, want an error", tc.in, got)
				}
				if !strings.Contains(err.Error(), "INFRA_MCP_TEST_UNSET") {
					t.Errorf("error %q does not name the variable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandString(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("expandString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExpandWalksTheDocument(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_SET", "value")

	var raw any
	if err := json.Unmarshal([]byte(`{"a":{"b":"${INFRA_MCP_TEST_SET}"},"c":["${INFRA_MCP_TEST_SET}"],"n":1}`), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := expand(raw)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	want := map[string]any{
		"a": map[string]any{"b": "value"},
		"c": []any{"value"},
		"n": float64(1),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expand = %#v, want %#v", got, want)
	}
}

func TestExpandNamesTheFailingKey(t *testing.T) {
	if err := os.Unsetenv("INFRA_MCP_TEST_UNSET"); err != nil {
		t.Fatal(err)
	}

	var raw any
	if err := json.Unmarshal([]byte(`{"connection":{"password":"${INFRA_MCP_TEST_UNSET}"}}`), &raw); err != nil {
		t.Fatal(err)
	}
	_, err := expand(raw)
	if err == nil {
		t.Fatal("expand succeeded with an unset variable")
	}
	if !strings.Contains(err.Error(), "connection: password") {
		t.Errorf("error %q does not locate the key", err)
	}
}

// seg spells a path template: a name is a key, "*" is every element of the
// container under the previous key.
func seg(names ...string) []segment {
	out := make([]segment, len(names))
	for i, n := range names {
		if n == "*" {
			out[i] = segment{each: true}
			continue
		}
		out[i] = segment{key: n}
	}
	return out
}

func TestSecretPaths(t *testing.T) {
	cluster := [][]segment{
		seg("connection", "password"),
		seg("brokers", "*", "password"),
		seg("shards", "*", "password"),
		seg("fallback", "password"),
	}

	// Every cluster path shows up twice: once under the core's own map of
	// environments, once at the level the source's keys occupy. The first copy
	// never matches anything — checkSecrets runs on a level with the
	// environments already taken out of it — and keeping it costs nothing,
	// while excluding it would cost the core a tag to say so.
	var want [][]segment
	for _, p := range cluster {
		want = append(want, append(seg("environments", "*", "clusters", "*"), p...))
	}
	want = append(want, cluster...)

	got := secretPaths[testConfig]()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secretPaths = %v, want %v", got, want)
	}
}

func TestSecretPathsStopsAtACycle(t *testing.T) {
	got := secretPaths[testChain]()
	want := [][]segment{seg("connection", "password")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secretPaths = %v, want %v", got, want)
	}
}

// A container reaching itself is a cycle no struct boundary interrupts; the
// walk has to end on its own, and the test hangs rather than fails if it does not.
func TestSecretPathsStopsAtAContainerCycle(t *testing.T) {
	if got := secretPaths[testGrove](); got != nil {
		t.Errorf("secretPaths = %v, want none", got)
	}
}

// The tag says the strings are secrets; the list holding them is not a place a
// ${VAR} can be written, so the path has to reach through it.
func TestSecretPathsReachThroughATaggedContainer(t *testing.T) {
	got := secretPaths[testBundle]()
	want := [][]segment{seg("passwords", "*")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secretPaths = %v, want %v", got, want)
	}

	var raw any
	if err := json.Unmarshal([]byte(`{"passwords":["${PGPASSWORD}","hunter2"]}`), &raw); err != nil {
		t.Fatal(err)
	}
	err := checkSecrets(raw, got)
	if err == nil {
		t.Fatal("checkSecrets accepted a literal inside a tagged list")
	}
	if !strings.HasPrefix(err.Error(), "passwords[1] must be") {
		t.Errorf("error %q does not name passwords[1]", err)
	}
}

func TestCheckSecrets(t *testing.T) {
	paths := [][]segment{seg("connection", "password")}
	for _, tc := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "reference", value: "${PGPASSWORD}"},
		{name: "literal", value: "hunter2", wantErr: true},
		{name: "reference with default", value: "${PGPASSWORD:-hunter2}", wantErr: true},
		{name: "reference inside a string", value: "prefix-${PGPASSWORD}", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"connection": map[string]any{"password": tc.value}}
			err := checkSecrets(raw, paths)
			if tc.wantErr != (err != nil) {
				t.Fatalf("checkSecrets(%q) error = %v, wantErr = %v", tc.value, err, tc.wantErr)
			}
		})
	}
}

// A container the walk steps through has to say which element it stopped at:
// "one of the brokers" sends nobody to a line of the file.
func TestCheckSecretsNamesThePlaceItStopped(t *testing.T) {
	for _, tc := range []struct {
		name string
		path []segment
		doc  string
		want string // the place the message must name, empty when there is none
	}{
		{
			name: "literal in a list",
			path: seg("brokers", "*", "password"),
			doc:  `{"brokers":[{"password":"${PGPASSWORD}"},{"password":"hunter2"}]}`,
			want: "brokers[1].password",
		},
		{
			name: "literal in a map",
			path: seg("shards", "*", "password"),
			doc:  `{"shards":{"a":{"password":"${PGPASSWORD}"},"b":{"password":"hunter2"}}}`,
			want: "shards.b.password",
		},
		{
			name: "literal behind a pointer",
			path: seg("fallback", "password"),
			doc:  `{"fallback":{"password":"hunter2"}}`,
			want: "fallback.password",
		},
		{
			name: "references throughout",
			path: seg("brokers", "*", "password"),
			doc:  `{"brokers":[{"password":"${PGPASSWORD}"},{"password":"${OTHER}"}]}`,
		},
		{
			name: "a container the document leaves out",
			path: seg("brokers", "*", "password"),
			doc:  `{"connection":{"host":"h"}}`,
		},
		{
			// null is what an absent pointer decodes from, and it is not a place
			// to look for a string.
			name: "null where a group would be",
			path: seg("fallback", "password"),
			doc:  `{"fallback":null}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw any
			if err := json.Unmarshal([]byte(tc.doc), &raw); err != nil {
				t.Fatal(err)
			}
			err := checkSecrets(raw, [][]segment{tc.path})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("checkSecrets = %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkSecrets accepted a literal at %s", tc.want)
			}
			if !strings.HasPrefix(err.Error(), tc.want+" must be") {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestDurationRoundTrip(t *testing.T) {
	b, err := json.Marshal(Duration(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"30s"` {
		t.Fatalf("marshalled as %s, want \"30s\"", b)
	}

	var d Duration
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.Duration() != 30*time.Second {
		t.Errorf("round trip gave %v", d.Duration())
	}
}

func TestInitWritesMinimalAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	loc := Location{Source: "test", Flag: filepath.Join(dir, "test.json")}

	minimal := testConfig{testCluster: testCluster{
		Connection: testConnection{Host: "db.example.com", Password: "${PGPASSWORD}"},
	}}
	path, err := Init(loc, minimal, "https://example.com/schema.json")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Init wrote invalid JSON: %v", err)
	}
	if got["$schema"] != "https://example.com/schema.json" {
		t.Errorf("$schema = %v", got["$schema"])
	}
	// Nothing but the required keys: a file full of defaults would freeze them.
	for _, unwanted := range []string{"output", "tools"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("Init wrote %q; it must write the minimum only", unwanted)
		}
	}

	if _, err := Init(loc, minimal, "https://example.com/schema.json"); err == nil {
		t.Error("Init overwrote an existing file")
	}
}

// Init writing the XDG file while the environment variable points elsewhere
// would report success on a file the server never reads.
func TestInitWritesWhereResolveWouldLook(t *testing.T) {
	dir := t.TempDir()
	loc := Location{Source: "test"}
	want := filepath.Join(dir, "from-env.json")
	t.Setenv(loc.EnvVar(), want)

	got, err := Init(loc, testConfig{}, "https://example.com/schema.json")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if got != want {
		t.Fatalf("Init wrote %q, want the path %s points at", got, loc.EnvVar())
	}
}

func TestLoadReportsMissingFileAsConfigError(t *testing.T) {
	tests := []struct {
		name  string
		named func(t *testing.T, loc *Location, path string)
	}{
		{"flag", func(_ *testing.T, loc *Location, path string) { loc.Flag = path }},
		{"env", func(t *testing.T, loc *Location, path string) { t.Setenv(loc.EnvVar(), path) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The XDG file is there and still must not be the one reported.
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			loc := Location{Source: "test"}
			writeFile(t, loc.XDGPath(), "{}")
			absent := filepath.Join(t.TempDir(), "absent.json")
			tt.named(t, &loc, absent)

			_, err := Load(loc, testConfig{}, nil)
			var cerr *ConfigError
			if !errors.As(err, &cerr) {
				t.Fatalf("Load error = %v, want a *ConfigError", err)
			}
			if cerr.Path != absent {
				t.Errorf("Path = %q, want the named %q", cerr.Path, absent)
			}
			if len(cerr.Searched) != 1 || !strings.Contains(cerr.Searched[0], absent) {
				t.Errorf("Searched = %v, want only the named path", cerr.Searched)
			}
			if cerr.Hint != initHint {
				t.Errorf("Hint = %q, want %q", cerr.Hint, initHint)
			}
		})
	}
}

// The promise is about the config, not about the walk: a literal anywhere a
// source may keep a secret has to come back as a config error naming the place.
func TestLoadRefusesALiteralInsideAContainer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster string
		want    string
	}{
		{
			name:    "list",
			cluster: `{"brokers":[{"password":"${PGPASSWORD}"},{"password":"hunter2"}]}`,
			want:    "brokers[1].password",
		},
		{
			name:    "map",
			cluster: `{"shards":{"a":{"password":"hunter2"}}}`,
			want:    "shards.a.password",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.json")
			writeFile(t, path, `{`+testEnvironments(tc.cluster)+`}`)

			_, err := Load(Location{Source: "test", Flag: path}, testConfig{}, nil)
			var cerr *ConfigError
			if !errors.As(err, &cerr) {
				t.Fatalf("Load error = %v, want a *ConfigError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// --init refuses to overwrite, so sending the operator there would strand them.
func TestLoadHintsAtTheSchemaWhenTheFileExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.json")
	writeFile(t, path, `{`+testEnvironments(`{"host":"h"}`)+`,"nope":1}`)
	loc := Location{Source: "test", Flag: path}

	_, err := Load(loc, testConfig{}, nil)
	var cerr *ConfigError
	if !errors.As(err, &cerr) {
		t.Fatalf("Load error = %v, want a *ConfigError", err)
	}
	if cerr.Hint != schemaHint {
		t.Errorf("Hint = %q, want %q", cerr.Hint, schemaHint)
	}
}

// Unreadable is still a file --init will not overwrite, so it takes the same
// hint as a file we could read and could not use.
func TestLoadHintsAtTheSchemaWhenTheFileCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	path := filepath.Join(t.TempDir(), "test.json")
	writeFile(t, path, `{}`)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	loc := Location{Source: "test", Flag: path}

	_, err := Load(loc, testConfig{}, nil)
	var cerr *ConfigError
	if !errors.As(err, &cerr) {
		t.Fatalf("Load error = %v, want a *ConfigError", err)
	}
	if cerr.Hint != schemaHint {
		t.Errorf("Hint = %q, want %q", cerr.Hint, schemaHint)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

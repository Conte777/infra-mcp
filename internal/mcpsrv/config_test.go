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
	Host     string `json:"host"`
	Password string `json:"password" mcpsrv:"secret"`
}

type testConfig struct {
	Common[testReadTools]

	Connection testConnection `json:"connection"`
}

func TestLocationResolvePrefersFlag(t *testing.T) {
	loc := Location{Source: "postgres", Profile: "default", Flag: "/given/by/flag.json"}
	t.Setenv(loc.EnvVar(), "/from/env.json")

	path, searched, err := loc.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != loc.Flag {
		t.Errorf("path = %q, want %q", path, loc.Flag)
	}
	// A named file that does not exist must not fall through to the env or XDG one.
	if len(searched) != 1 {
		t.Errorf("searched = %v, want only the flag", searched)
	}
}

func TestLocationResolveOrder(t *testing.T) {
	dir := t.TempDir()
	loc := Location{Source: "postgres", Profile: "default"}

	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeFile(t, filepath.Join(xdg, "infra-mcp", "postgres.default.json"), "{}")

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
	loc := Location{Source: "postgres", Profile: "default"}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "empty"))

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

func TestSecretPaths(t *testing.T) {
	got := secretPaths[testConfig]()
	want := [][]string{{"connection", "password"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secretPaths = %v, want %v", got, want)
	}
}

func TestCheckSecrets(t *testing.T) {
	paths := [][]string{{"connection", "password"}}
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
	loc := Location{Source: "test", Profile: "default", Flag: filepath.Join(dir, "test.default.json")}

	minimal := testConfig{Connection: testConnection{Host: "db.example.com", Password: "${PGPASSWORD}"}}
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
	loc := Location{Source: "test", Profile: "default"}
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
	loc := Location{Source: "test", Profile: "default", Flag: filepath.Join(t.TempDir(), "absent.json")}

	_, err := Load(loc, testConfig{}, nil)
	var cerr *ConfigError
	if !errors.As(err, &cerr) {
		t.Fatalf("Load error = %v, want a *ConfigError", err)
	}
	if cerr.Hint != initHint {
		t.Errorf("Hint = %q, want %q", cerr.Hint, initHint)
	}
}

// --init refuses to overwrite, so sending the operator there would strand them.
func TestLoadHintsAtTheSchemaWhenTheFileExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.default.json")
	writeFile(t, path, `{"connection":{"host":"h","password":"${P}"},"nope":1}`)
	loc := Location{Source: "test", Profile: "default", Flag: path}

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
	path := filepath.Join(t.TempDir(), "test.default.json")
	writeFile(t, path, `{}`)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	loc := Location{Source: "test", Profile: "default", Flag: path}

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

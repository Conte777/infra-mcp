package postgres_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/source/postgres"
)

var update = flag.Bool("update", false, "rewrite the committed schema instead of comparing against it")

// schemaFile is what editors fetch from the published repo; the test below is
// the gate that keeps it in step with the Go type.
const schemaFile = "../../../schema/postgres.schema.json"

const validConfig = `{
  "connection": {"host": "db.example.com", "user": "app", "password": "${INFRA_MCP_TEST_PASSWORD}"},
  "databases": {"default": "app_db"}
}`

func TestSchemaMatchesCommittedFile(t *testing.T) {
	var buf bytes.Buffer
	if err := mcpsrv.PrintSchema[postgres.Config](&buf, postgres.ConfigTypes()); err != nil {
		t.Fatalf("PrintSchema: %v", err)
	}

	if *update {
		if err := os.WriteFile(schemaFile, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("%s is out of step with postgres.Config; run `task schema`", schemaFile)
	}
}

func TestLoadAppliesFileOnTopOfDefaults(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_PASSWORD", "secret")
	loc := configAt(t, validConfig)

	cfg, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Connection.Password != "secret" {
		t.Errorf("password = %q, want the expanded value", cfg.Connection.Password)
	}
	if cfg.Connection.Port != 5432 {
		t.Errorf("port = %d, want the default", cfg.Connection.Port)
	}
	if cfg.Timeouts.Query.Duration() != 30*time.Second {
		t.Errorf("timeouts.query = %v, want the default", cfg.Timeouts.Query.Duration())
	}
	if !cfg.Tools.Write.RequireConfirmation {
		t.Error("tools.write.requireConfirmation lost its default of true")
	}
	if cfg.Output.MaxRows != 200 {
		t.Errorf("output.maxRows = %d, want the default", cfg.Output.MaxRows)
	}
	if got := cfg.ClientDeadline(); got != 35*time.Second {
		t.Errorf("ClientDeadline = %v, want query + 5s", got)
	}
}

func TestLoadKeepsAnExplicitFalse(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_PASSWORD", "secret")
	loc := configAt(t, `{
	  "connection": {"host": "h", "user": "u", "password": "${INFRA_MCP_TEST_PASSWORD}"},
	  "databases": {"default": "d"},
	  "tools": {"write": {"requireConfirmation": false}}
	}`)

	cfg, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tools.Write.RequireConfirmation {
		t.Error("an explicit false was overridden by the default true")
	}
}

func TestLoadRejects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		reason string
	}{
		{
			name:   "unknown key",
			body:   `{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"output":{"maxRow":10}}`,
			reason: "maxRow",
		},
		{
			name:   "missing required key",
			body:   `{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"}}`,
			reason: "databases",
		},
		{
			name:   "literal password",
			body:   `{"connection":{"host":"h","user":"u","password":"hunter2"},"databases":{"default":"d"}}`,
			reason: "connection.password",
		},
		{
			name:   "duration without a unit",
			body:   `{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"timeouts":{"query":"30"}}`,
			reason: "query",
		},
		{
			name:   "unknown sslmode",
			body:   `{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}","sslmode":"maybe"},"databases":{"default":"d"}}`,
			reason: "sslmode",
		},
		{
			name:   "exclude hiding the default database",
			body:   `{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"app_db","showAll":true,"exclude":["app_*"]}}`,
			reason: "databases.exclude",
		},
		{
			name:   "a pool of no databases",
			body:   `{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"pool":{"maxDatabases":0}}`,
			reason: "pool.maxDatabases",
		},
		{
			name:   "more connections per database than anyone wants",
			body:   `{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"pool":{"maxConnsPerDatabase":5000}}`,
			reason: "pool.maxConnsPerDatabase",
		},
		{
			name:   "not JSON",
			body:   `{`,
			reason: "JSON",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INFRA_MCP_TEST_PASSWORD", "secret")
			loc := configAt(t, tc.body)

			_, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes())
			var cerr *mcpsrv.ConfigError
			if !errors.As(err, &cerr) {
				t.Fatalf("Load error = %v, want a *mcpsrv.ConfigError", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error %q does not mention %q", err, tc.reason)
			}
		})
	}
}

func TestLoadNamesAnUnsetVariable(t *testing.T) {
	if err := os.Unsetenv("INFRA_MCP_TEST_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	loc := configAt(t, validConfig)

	_, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes())
	if err == nil {
		t.Fatal("Load succeeded with an unset password variable")
	}
	if !strings.Contains(err.Error(), "INFRA_MCP_TEST_PASSWORD") {
		t.Errorf("error %q does not name the variable", err)
	}
}

func TestInitWritesAConfigThatLoads(t *testing.T) {
	t.Setenv("PGPASSWORD", "secret")
	loc := mcpsrv.Location{
		Source:  postgres.Name,
		Profile: "default",
		Flag:    filepath.Join(t.TempDir(), "postgres.default.json"),
	}

	path, err := mcpsrv.Init(loc, postgres.Minimal(), mcpsrv.SchemaURL(postgres.Name))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var written map[string]any
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("Init wrote invalid JSON: %v", err)
	}
	if _, ok := written["pool"]; ok {
		t.Error("Init wrote the pool defaults; the file must carry the minimum only")
	}

	if _, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes()); err != nil {
		t.Errorf("the file Init wrote does not load: %v", err)
	}
}

func configAt(t *testing.T, body string) mcpsrv.Location {
	t.Helper()
	path := filepath.Join(t.TempDir(), "postgres.default.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return mcpsrv.Location{Source: postgres.Name, Profile: "default", Flag: path}
}

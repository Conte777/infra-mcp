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

// devMain is the address the fixtures below declare.
var devMain = mcpsrv.Address{Environment: "dev", Cluster: "main"}

// oneCluster frames a cluster body as a whole config file: everything above the
// cluster is the shortest thing that declares an address to reach.
func oneCluster(body string) string {
	return `{"environments":{"dev":{"clusters":{"main":` + body + `}}}}`
}

const validCluster = `{
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

	cfg := mustLoad(t, oneCluster(validCluster)).Config

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

// The three levels are the point of the file: what a cluster does not say it
// takes from its environment, and what neither says from the global level.
func TestLoadInheritsDownTheLevels(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_PASSWORD", "secret")
	inv := load(t, `{
	  "connection": {"user": "app", "password": "${INFRA_MCP_TEST_PASSWORD}"},
	  "environments": {
	    "dev": {
	      "connection": {"host": "dev.example.com"},
	      "clusters": {
	        "main":     {"databases": {"default": "app_db"}},
	        "reporting": {"connection": {"host": "reports.example.com"}, "databases": {"default": "reports"}}
	      }
	    }
	  }
	}`)

	if len(inv.Clusters) != 2 {
		t.Fatalf("loaded %d clusters, want 2", len(inv.Clusters))
	}
	main := find(t, inv, devMain)
	if main.Connection.Host != "dev.example.com" {
		t.Errorf("dev/main host = %q, want the environment's", main.Connection.Host)
	}
	if main.Connection.User != "app" {
		t.Errorf("dev/main user = %q, want the global one", main.Connection.User)
	}

	reporting := find(t, inv, mcpsrv.Address{Environment: "dev", Cluster: "reporting"})
	if reporting.Connection.Host != "reports.example.com" {
		t.Errorf("dev/reporting host = %q, want its own", reporting.Connection.Host)
	}
	if reporting.Connection.User != "app" {
		t.Errorf("dev/reporting user = %q, want the global one", reporting.Connection.User)
	}
}

// readOnly inherits like everything else, and a cluster may take it back: an
// environment marked read-only with one writable cluster in it is a config
// somebody will write.
func TestLoadInheritsReadOnly(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_PASSWORD", "secret")
	inv := load(t, `{
	  "connection": {"host": "h", "user": "u", "password": "${INFRA_MCP_TEST_PASSWORD}"},
	  "databases": {"default": "d"},
	  "environments": {
	    "prod": {
	      "readOnly": true,
	      "clusters": {"main": {}, "sandbox": {"readOnly": false}}
	    }
	  }
	}`)

	for _, tc := range []struct {
		cluster string
		want    bool
	}{
		{"main", true},
		{"sandbox", false},
	} {
		addr := mcpsrv.Address{Environment: "prod", Cluster: tc.cluster}
		cluster, err := inv.Find(addr)
		if err != nil {
			t.Fatalf("Find(%s): %v", addr, err)
		}
		if cluster.ReadOnly != tc.want {
			t.Errorf("%s readOnly = %v, want %v", addr, cluster.ReadOnly, tc.want)
		}
	}
}

func TestLoadKeepsAnExplicitFalse(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_PASSWORD", "secret")
	inv := load(t, `{
	  "tools": {"write": {"requireConfirmation": false}},
	  "environments": {"dev": {"clusters": {"main": `+validCluster+`}}}
	}`)

	if inv.Global.Tools.Write.RequireConfirmation {
		t.Error("an explicit false was overridden by the default true")
	}
}

// A 0.1 file is recognised by what it lacks, before the schema gets to answer
// it with a heap of unknown-key complaints that never say what replaced them.
func TestLoadExplainsTheOldShape(t *testing.T) {
	loc := configAt(t, `{
	  "connection": {"host": "h", "user": "u", "password": "${INFRA_MCP_TEST_PASSWORD}"},
	  "databases": {"default": "d"}
	}`)

	_, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes())
	var cerr *mcpsrv.ConfigError
	if !errors.As(err, &cerr) {
		t.Fatalf("Load error = %v, want a *mcpsrv.ConfigError", err)
	}
	for _, want := range []string{"environments", "clusters"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
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
			body:   `{"output":{"maxRow":10},"environments":{"dev":{"clusters":{"main":` + validCluster + `}}}}`,
			reason: "maxRow",
		},
		{
			name:   "a global key inside a cluster",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"output":{"maxRows":10}}`),
			reason: "output",
		},
		{
			name:   "an environment with no clusters",
			body:   `{"environments":{"dev":{"clusters":{}}}}`,
			reason: "no clusters",
		},
		{
			name:   "a key nothing supplies at any level",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"}}`),
			reason: "databases.default",
		},
		{
			name:   "literal password",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"hunter2"},"databases":{"default":"d"}}`),
			reason: "connection.password",
		},
		{
			name:   "duration without a unit",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"timeouts":{"query":"30"}}`),
			reason: "query",
		},
		{
			name:   "unknown sslmode",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}","sslmode":"maybe"},"databases":{"default":"d"}}`),
			reason: "sslmode",
		},
		{
			name:   "exclude hiding the default database",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"app_db","showAll":true,"exclude":["app_*"]}}`),
			reason: "databases.exclude",
		},
		{
			name:   "a pool of no databases",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"pool":{"maxDatabases":0}}`),
			reason: "pool.maxDatabases",
		},
		{
			name:   "more connections per database than anyone wants",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"pool":{"maxConnsPerDatabase":5000}}`),
			reason: "pool.maxConnsPerDatabase",
		},
		{
			name:   "a query with no limit",
			body:   oneCluster(`{"connection":{"host":"h","user":"u","password":"${INFRA_MCP_TEST_PASSWORD}"},"databases":{"default":"d"},"timeouts":{"query":"0s"}}`),
			reason: "timeouts.query",
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

// Which cluster is unusable is the whole of the diagnosis when there are ten of
// them and one has a typo in it.
func TestLoadNamesTheClusterItRefused(t *testing.T) {
	t.Setenv("INFRA_MCP_TEST_PASSWORD", "secret")
	loc := configAt(t, `{
	  "connection": {"host": "h", "user": "u", "password": "${INFRA_MCP_TEST_PASSWORD}"},
	  "environments": {"stage": {"clusters": {"reporting": {"pool": {"maxDatabases": 0}}}}}
	}`)

	_, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes())
	if err == nil {
		t.Fatal("Load accepted a cluster with an empty pool")
	}
	if !strings.Contains(err.Error(), "stage/reporting") {
		t.Errorf("error %q does not name the cluster", err)
	}
}

func TestLoadNamesAnUnsetVariable(t *testing.T) {
	if err := os.Unsetenv("INFRA_MCP_TEST_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	loc := configAt(t, oneCluster(validCluster))

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
		Source: postgres.Name,
		Flag:   filepath.Join(t.TempDir(), "postgres.json"),
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
	if _, ok := written["environments"]; !ok {
		t.Error("Init wrote no environments, so the file it wrote reaches nothing")
	}

	if _, err := mcpsrv.Load(loc, postgres.Defaults(), postgres.ConfigTypes()); err != nil {
		t.Errorf("the file Init wrote does not load: %v", err)
	}
}

func configAt(t *testing.T, body string) mcpsrv.Location {
	t.Helper()
	path := filepath.Join(t.TempDir(), "postgres.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return mcpsrv.Location{Source: postgres.Name, Flag: path}
}

func load(t *testing.T, body string) mcpsrv.Inventory[postgres.Config] {
	t.Helper()
	inv, err := mcpsrv.Load(configAt(t, body), postgres.Defaults(), postgres.ConfigTypes())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return inv
}

func mustLoad(t *testing.T, body string) mcpsrv.Cluster[postgres.Config] {
	t.Helper()
	cluster, err := load(t, body).Find(devMain)
	if err != nil {
		t.Fatalf("Find(%s): %v", devMain, err)
	}
	return cluster
}

func find(t *testing.T, inv mcpsrv.Inventory[postgres.Config], addr mcpsrv.Address) postgres.Config {
	t.Helper()
	cluster, err := inv.Find(addr)
	if err != nil {
		t.Fatalf("Find(%s): %v", addr, err)
	}
	return cluster.Config
}

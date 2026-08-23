//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/pgtest"
)

// The three addresses the config below declares, and a database each of them
// does reach — which for dev/reporting is not the one it enters through.
var (
	devMain      = mcpsrv.Address{Environment: "dev", Cluster: "main"}
	devReporting = mcpsrv.Address{Environment: "dev", Cluster: "reporting"}
	prodMain     = mcpsrv.Address{Environment: "prod", Cluster: "main"}
)

var reachable = map[mcpsrv.Address]string{
	devMain:      "dev_main",
	devReporting: "dev_reporting",
	prodMain:     "prod_main",
}

const passwordVar = "INFRA_MCP_ADDRESS_PASSWORD"

// addressConfig is two environments and three clusters over one postgres: which
// host a cluster points at is not what these tests are about — which config a
// call lands on is. dev/reporting keeps the entry point out of its include, so
// the database its catalog query runs in is one it cannot be sent into.
const addressConfig = `{
  "connection": {"host": %q, "port": %d, "user": %q, "password": "${` + passwordVar + `}", "sslmode": "disable"},
  "environments": {
    "dev": {
      "clusters": {
        "main":      {"databases": {"default": "dev_main"}},
        "reporting": {"databases": {"include": ["dev_reporting"]}}
      }
    },
    "prod": {
      "readOnly": true,
      "clusters": {"main": {"databases": {"default": "prod_main"}}}
    }
  }
}`

// addressed brings up the container, creates a database per cluster and serves
// the whole thing the way a client sees it: the core resolves the address, and
// nothing here reaches past it into a handler.
func addressed(t *testing.T) *mcp.ClientSession {
	t.Helper()

	dsn := pgtest.Start(t)
	parsed, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the harness DSN: %v", err)
	}
	admin := Connection{
		Host:     parsed.Host,
		Port:     int(parsed.Port),
		User:     parsed.User,
		Password: parsed.Password,
		SSLMode:  SSLDisable,
	}
	// CREATE DATABASE runs in no transaction, so it goes nowhere near the tools.
	createDatabases(t, admin, parsed.Database, "dev_main", "dev_reporting", "prod_main")

	t.Setenv(passwordVar, admin.Password)
	path := filepath.Join(t.TempDir(), "postgres.json")
	body := fmt.Sprintf(addressConfig, admin.Host, admin.Port, admin.User)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	loc := mcpsrv.Location{Source: Name, Flag: path}
	inv, err := mcpsrv.Load(loc, Defaults(), ConfigTypes())
	if err != nil {
		t.Fatalf("load the three-cluster config: %v", err)
	}
	if len(inv.Clusters) != len(reachable) {
		t.Fatalf("loaded %d clusters, want %d", len(inv.Clusters), len(reachable))
	}

	spec := Spec()
	rt := mcpsrv.NewRuntime[Config, *Config](inv, nil, mcpsrv.Process{Source: Name}, nil)
	server := mcpsrv.Build(spec, rt)
	t.Cleanup(func() {
		if err := spec.Source.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "addrtest", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func createDatabases(t *testing.T, c Connection, from string, names ...string) {
	t.Helper()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connString(c, from))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for _, name := range names {
		if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
}

// args is one tool call's arguments: the address, plus whatever the tool takes
// on top of it.
func args(a mcpsrv.Address, rest map[string]any) map[string]any {
	out := map[string]any{"environment": a.Environment, "cluster": a.Cluster}
	for k, v := range rest {
		out[k] = v
	}
	return out
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, in map[string]any) (string, bool) {
	t.Helper()

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: in})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func mustCall(t *testing.T, cs *mcp.ClientSession, name string, in map[string]any) string {
	t.Helper()
	out, isError := callTool(t, cs, name, in)
	if isError {
		t.Fatalf("%s%v failed: %s", name, in, out)
	}
	return out
}

// firstColumn reads a rendered markdown table back: what the model gets is text,
// and a test that read the blocks would be testing a different thing.
func firstColumn(t *testing.T, out string) []string {
	t.Helper()

	var rows []string
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i < 2 || !strings.HasPrefix(line, "|") {
			continue // the header and the separator under it
		}
		rows = append(rows, strings.Split(strings.Trim(line, "|"), "|")[0])
	}
	return rows
}

// The address is two arguments and the config behind it is three levels deep;
// what proves they are wired together is a call landing in the right database.
func TestEveryAddressRunsInItsOwnCluster(t *testing.T) {
	cs := addressed(t)

	for addr, want := range reachable {
		out := mustCall(t, cs, "pg_read_query", args(addr, map[string]any{
			"database": want,
			"sql":      "SELECT current_database() AS db",
		}))
		if got := firstColumn(t, out); len(got) != 1 || got[0] != want {
			t.Errorf("%s ran in %v, want %q", addr, got, want)
		}
	}
}

func TestAnUnknownAddressIsRefused(t *testing.T) {
	cs := addressed(t)

	out, isError := callTool(t, cs, "pg_read_query", args(
		mcpsrv.Address{Environment: "dev", Cluster: "nowhere"},
		map[string]any{"database": "dev_main", "sql": "SELECT 1"}))
	if !isError {
		t.Fatalf("a cluster nobody configured answered: %s", out)
	}
	if !strings.Contains(out, "dev/nowhere") {
		t.Errorf("the refusal does not name the address: %s", out)
	}
}

// include is the whole of "which databases this address shows", and the entry
// point its catalog query runs in is not exempt from it.
func TestIncludeBoundsWhatAnAddressReaches(t *testing.T) {
	cs := addressed(t)

	got := firstColumn(t, mustCall(t, cs, "pg_read_list_databases", args(devReporting, nil)))
	if len(got) != 1 || got[0] != "dev_reporting" {
		t.Errorf("dev/reporting lists %v, want only dev_reporting", got)
	}

	out, isError := callTool(t, cs, "pg_read_query", args(devReporting, map[string]any{
		"database": entryDatabase,
		"sql":      "SELECT 1",
	}))
	if !isError {
		t.Fatalf("the entry point took a query although include does not name it: %s", out)
	}
	if !strings.Contains(out, "databases.include") {
		t.Errorf("the refusal does not name the list that refused: %s", out)
	}

	// A cluster that names no include reaches its whole postgres.
	if got := firstColumn(t, mustCall(t, cs, "pg_read_list_databases", args(devMain, nil))); len(got) < 2 {
		t.Errorf("dev/main lists %v, want the whole cluster", got)
	}
}

// readOnly is a refusal by address, not a tool the config hides: the tool set is
// what a permissions allow-list globs over, and it may not depend on the config.
func TestWriteAtAReadOnlyAddressIsRefused(t *testing.T) {
	cs := addressed(t)

	const ddl = "CREATE TABLE landed (id int)"
	out, isError := callTool(t, cs, "pg_write_execute", args(prodMain, map[string]any{
		"database": "prod_main",
		"sql":      ddl,
	}))
	if !isError {
		t.Fatalf("a write landed on a readOnly address: %s", out)
	}
	if !strings.Contains(out, "readOnly") {
		t.Errorf("the refusal does not say what refused it: %s", out)
	}

	// The same call one environment over, to show it is the address that decided.
	mustCall(t, cs, "pg_write_execute", args(devMain, map[string]any{
		"database": "dev_main",
		"sql":      ddl,
	}))

	got := firstColumn(t, mustCall(t, cs, "pg_read_query", args(prodMain, map[string]any{
		"database": "prod_main",
		"sql":      "SELECT count(*) AS n FROM information_schema.tables WHERE table_name = 'landed'",
	})))
	if len(got) != 1 || got[0] != "0" {
		t.Errorf("landed count = %v, want [0] — the refused write reached prod anyway", got)
	}

	// A read tool at the same address is untouched.
	mustCall(t, cs, "pg_read_query", args(prodMain, map[string]any{
		"database": "prod_main",
		"sql":      "SELECT 1",
	}))
}

package postgres_test

import (
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/source/postgres"
)

func listTools(t *testing.T) []*mcp.Tool {
	t.Helper()

	spec := postgres.Spec()
	rt := mcpsrv.NewRuntime(mcpsrv.Inventory[postgres.Config]{Global: spec.Defaults}, nil,
		mcpsrv.Process{Source: spec.Name, Transport: "stdio"}, nil)

	ct, st := mcp.NewInMemoryTransports()
	if _, err := mcpsrv.Build(spec, rt).Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("server.Connect() = %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "toolstest", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client.Connect() = %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list = %v", err)
	}
	return res.Tools
}

// The set is fixed by ADR-0001, and the names are what a permissions allow-list
// globs over: a rename is a change to what the user has to write in settings.json.
func TestToolSet(t *testing.T) {
	want := []string{
		"pg_read_describe_table",
		"pg_read_explain",
		"pg_read_list_databases",
		"pg_read_list_tables",
		"pg_read_query",
		"pg_read_status",
		"pg_write_execute",
	}

	var got []string
	for _, tool := range listTools(t) {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

// Every argument that says where a call lands is required and none is
// defaulted, which is what keeps "which cluster" and "which database" out of
// the server's state (ADR-0001). They also all arrive by embedding, so this is
// the check that the embedded structs are inlined into the schema rather than
// nested under a key.
func TestRequiredArguments(t *testing.T) {
	want := map[string][]string{
		"pg_read_list_databases": {"cluster", "environment"},
		"pg_read_list_tables":    {"cluster", "database", "environment"},
		"pg_read_describe_table": {"cluster", "database", "environment", "tables"},
		"pg_read_query":          {"cluster", "database", "environment", "sql"},
		"pg_read_explain":        {"cluster", "database", "environment", "sql"},
		"pg_write_execute":       {"cluster", "database", "environment", "sql"},
		"pg_read_status":         nil,
	}

	for _, tool := range listTools(t) {
		got := required(t, tool)
		slices.Sort(got)
		expected := slices.Clone(want[tool.Name])
		slices.Sort(expected)
		if !slices.Equal(got, expected) {
			t.Errorf("%s requires %v, want %v", tool.Name, got, expected)
		}
	}
}

// The input schema comes off the wire as a decoded JSON object.
func required(t *testing.T, tool *mcp.Tool) []string {
	t.Helper()

	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("%s: input schema is %T, want an object", tool.Name, tool.InputSchema)
	}
	// Absent, not empty, when a tool takes no required argument.
	req, _ := schema["required"].([]any)
	var names []string
	for _, v := range req {
		name, ok := v.(string)
		if !ok {
			t.Fatalf("%s: required holds %T, want a string", tool.Name, v)
		}
		names = append(names, name)
	}
	return names
}

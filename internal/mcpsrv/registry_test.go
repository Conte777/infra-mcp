package mcpsrv

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

type testIn struct {
	Address

	Name string `json:"name" jsonschema:"who to greet"`
}

func noop(context.Context, testConfig, testIn) ([]block.Block, error) { return nil, nil }

func newTestRegistry(t *testing.T, rt Runtime[testConfig]) *Registry[testConfig] {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	return NewRegistry(server, "tt", rt)
}

func TestToolNamesAreAssembledFromPrefixAndAction(t *testing.T) {
	r := newTestRegistry(t, Runtime[testConfig]{})

	Read(r, "list_tables", "list the tables", noop)
	Write(r, "execute", "run a statement", noop)

	want := []string{"tt_read_list_tables", "tt_write_execute"}
	for i, tool := range r.Registered() {
		if tool.Name != want[i] {
			t.Fatalf("tool %d named %q, want %q", i, tool.Name, want[i])
		}
	}
}

func TestAccessDecidesTheHintAndTheMarker(t *testing.T) {
	rt := Runtime[testConfig]{}
	rt.Settings.Write.RequireConfirmation = true
	r := newTestRegistry(t, rt)

	Read(r, "query", "read", noop)
	Write(r, "execute", "write", noop)

	read, write := r.Registered()[0], r.Registered()[1]
	if !read.Annotations.ReadOnlyHint {
		t.Error("a read tool went out without ReadOnlyHint")
	}
	if _, ok := read.Meta[metaRequiresUserInteraction]; ok {
		t.Error("a read tool asks for confirmation, so it will never be auto-approved")
	}
	if write.Annotations.ReadOnlyHint {
		t.Error("a write tool went out marked read-only")
	}
	if write.Meta[metaRequiresUserInteraction] != true {
		t.Error("a write tool went out without the confirmation marker")
	}
}

func TestConfirmationMarkerIsRemovedByConfig(t *testing.T) {
	r := newTestRegistry(t, Runtime[testConfig]{})

	Write(r, "execute", "write", noop)

	if _, ok := r.Registered()[0].Meta[metaRequiresUserInteraction]; ok {
		t.Error("requireConfirmation is off and the marker stayed on")
	}
}

func TestActionOutsideTheNamingSchemeIsRejected(t *testing.T) {
	for _, action := range []string{"", "Query", "list tables", "list-tables", "1st"} {
		t.Run(action, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("action %q was accepted; the allow-list glob no longer covers it", action)
				}
			}()
			Read(newTestRegistry(t, Runtime[testConfig]{}), action, "", noop)
		})
	}
}

func TestDegradedStartAnswersWithoutReachingTheHandler(t *testing.T) {
	reached := false
	h := func(context.Context, testConfig, testIn) ([]block.Block, error) {
		reached = true
		return nil, nil
	}
	r := newTestRegistry(t, Runtime[testConfig]{Degraded: errors.New("no config: none found")})

	_, err := call(t.Context(), r, accessRead, h, testIn{})

	if reached {
		t.Fatal("the handler ran on a degraded start")
	}
	f := asFailure(err)
	if f.Kind != KindNotConfigured {
		t.Fatalf("Kind = %v, want KindNotConfigured", f.Kind)
	}
	if !strings.Contains(f.Detail, "no config") {
		t.Fatalf("Detail = %q, want the config diagnosis", f.Detail)
	}
}

// The config a handler gets is one cluster's, and the global keys ride along in
// it: a source reads its own global half — tools.read — off the same value.
func TestHandlerReceivesTheConfigOfTheAddressedCluster(t *testing.T) {
	var got testConfig
	h := func(_ context.Context, cfg testConfig, _ testIn) ([]block.Block, error) {
		got = cfg
		return nil, nil
	}
	cfg := testConfig{testCluster: testCluster{Connection: testConnection{Host: "db.example.com"}}}
	cfg.Tools.Read = testReadTools{Extra: []string{"one"}}
	addr := Address{Environment: "dev", Cluster: "main"}
	r := newTestRegistry(t, Runtime[testConfig]{Inventory: Inventory[testConfig]{
		Clusters: []Cluster[testConfig]{{Address: addr, Config: cfg}},
	}})

	if _, err := call(t.Context(), r, accessRead, h, testIn{Address: addr}); err != nil {
		t.Fatalf("call() = %v", err)
	}
	if got.Connection.Host != cfg.Connection.Host {
		t.Fatalf("handler saw %+v, want the loaded config", got)
	}
	if len(got.Tools.Read.Extra) != 1 {
		t.Errorf("handler saw tools.read = %+v, want the global half of the config", got.Tools.Read)
	}
}

// The failure text is rendered before SetError, which only fills Content when
// it is empty: were that to change, the model would read the bare error string
// instead of the blocks.
func TestSetErrorKeepsTheRenderedFailure(t *testing.T) {
	res := textResult("rendered")

	res.SetError(&Failure{Kind: KindTimeout})

	if !res.IsError {
		t.Error("IsError is false, so the model reads the failure as an answer")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	if text.Text != "rendered" {
		t.Fatalf("Content[0] = %q, want the rendered failure", text.Text)
	}
}

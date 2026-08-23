package mcpsrv

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

type fakeSource struct{}

func (*fakeSource) Prefix() string { return "tt" }

func (*fakeSource) Instructions() string { return "the fake source speaks here" }

func (*fakeSource) Tools(r *Registry[testConfig]) {
	Read(r, "rows", "hand back one row", func(context.Context, testConfig, testIn) ([]block.Block, error) {
		return []block.Block{block.Table{Columns: []string{"a"}, Rows: [][]any{{1}}}}, nil
	})
	Write(r, "execute", "change something", func(context.Context, testConfig, testIn) ([]block.Block, error) {
		return []block.Block{block.Text("done")}, nil
	})
}

func (*fakeSource) Close() error { return nil }

func testSpec() Spec[testConfig] {
	defaults := testConfig{}
	defaults.Output = Output{MaxRows: 10, MaxBytes: 4096, MaxCellChars: 100}
	defaults.Tools.Write.RequireConfirmation = true
	return Spec[testConfig]{Name: "fake", Source: &fakeSource{}, Defaults: defaults, Minimal: testConfig{}}
}

// testAddress is the one cluster every fake server in here serves.
var testAddress = Address{Environment: "dev", Cluster: "main"}

func testInventory(clusters ...Cluster[testConfig]) Inventory[testConfig] {
	if clusters == nil {
		clusters = []Cluster[testConfig]{{Address: testAddress, Config: testSpec().Defaults}}
	}
	return Inventory[testConfig]{Global: testSpec().Defaults, Clusters: clusters}
}

func testProcess() Process {
	return Process{Source: "fake", ConfigPath: "/tmp/fake.json", Transport: "stdio"}
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("server.Connect() = %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client.Connect() = %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func buildSession(t *testing.T, degraded error) *mcp.ClientSession {
	t.Helper()
	spec := testSpec()
	return connect(t, Build(spec, NewRuntime(testInventory(), degraded, testProcess(), nil)))
}

var readOnlyAddress = Address{Environment: "prod", Cluster: "analytics"}

// twoClusterSession serves one plain cluster and one readOnly, which is what
// the inventory has to tell apart.
func twoClusterSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	spec := testSpec()
	inv := testInventory(
		Cluster[testConfig]{Address: testAddress, Config: spec.Defaults},
		Cluster[testConfig]{Address: readOnlyAddress, Config: spec.Defaults, ReadOnly: true},
	)
	return connect(t, Build(spec, NewRuntime(inv, nil, testProcess(), nil)))
}

// The status tool takes no arguments, every other fake tool takes testIn.
func callTool(t *testing.T, cs *mcp.ClientSession, name string) *mcp.CallToolResult {
	t.Helper()
	var args map[string]any
	if !strings.HasSuffix(name, "_"+statusAction) {
		args = map[string]any{
			"name":        "world",
			"environment": testAddress.Environment,
			"cluster":     testAddress.Cluster,
		}
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("tools/call %s = %v", name, err)
	}
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("content is %T, want *mcp.TextContent", c)
		}
		b.WriteString(text.Text)
	}
	return b.String()
}

func TestInstructionsReachInitialize(t *testing.T) {
	cs := buildSession(t, nil)

	got := cs.InitializeResult().Instructions

	if !strings.Contains(got, (&fakeSource{}).Instructions()) {
		t.Fatalf("instructions = %q, want the source's text in them", got)
	}
}

// Three required arguments and no defaults: without this list the model cannot
// name an address, and so cannot call a single tool.
func TestInstructionsListEveryAddress(t *testing.T) {
	cs := twoClusterSession(t)

	got := cs.InitializeResult().Instructions

	if !strings.Contains(got, "dev/main\n") {
		t.Errorf("instructions = %q, want dev/main unmarked", got)
	}
	if !strings.Contains(got, "prod/analytics (readOnly)") {
		t.Errorf("instructions = %q, want the readOnly cluster marked", got)
	}
}

// The reason is left out on purpose: every tool call answers with it, and the
// instructions are paid for once per session whether or not it is read.
// The inventory carries clusters here, and the degraded start still may not
// advertise them: every call to one answers with the config error instead.
func TestDegradedInstructionsPointAtStatusWithoutTheReason(t *testing.T) {
	cs := buildSession(t, errors.New("no config: none found"))

	got := cs.InitializeResult().Instructions

	if !strings.Contains(got, "tt_read_status") {
		t.Errorf("instructions = %q, want them to point at the status tool", got)
	}
	if strings.Contains(got, "none found") {
		t.Errorf("instructions = %q, want the diagnosis left to the tool calls", got)
	}
	if strings.Contains(got, testAddress.String()) {
		t.Errorf("instructions = %q, want no address a call cannot reach", got)
	}
}

func TestToolsListCarriesTheSourceSetPlusStatus(t *testing.T) {
	cs := buildSession(t, nil)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list = %v", err)
	}

	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	want := "tt_read_rows,tt_read_status,tt_write_execute"
	if strings.Join(got, ",") != want {
		t.Fatalf("tools = %v, want %s", got, want)
	}
}

func TestAnswerIsMarkdownWithoutStructuredContent(t *testing.T) {
	cs := buildSession(t, nil)

	res := callTool(t, cs, "tt_read_rows")

	if res.IsError {
		t.Fatalf("tt_read_rows failed: %s", resultText(t, res))
	}
	if res.StructuredContent != nil {
		t.Errorf("structuredContent = %v; the answer is markdown and nothing else", res.StructuredContent)
	}
	if !strings.Contains(resultText(t, res), "|a|") {
		t.Errorf("content = %q, want a markdown table", resultText(t, res))
	}
}

func TestDegradedStartAnswersEveryCallWithTheDiagnosis(t *testing.T) {
	cs := buildSession(t, errors.New("no config: none found"))

	for _, name := range []string{"tt_read_rows", "tt_write_execute", "tt_read_status"} {
		res := callTool(t, cs, name)
		if !res.IsError {
			t.Errorf("%s answered normally on a degraded start", name)
			continue
		}
		if !strings.Contains(resultText(t, res), "no config: none found") {
			t.Errorf("%s answered %q, want the config diagnosis", name, resultText(t, res))
		}
	}
}

func TestStatusReportsTheConfigItSettledOn(t *testing.T) {
	cs := buildSession(t, nil)

	got := resultText(t, callTool(t, cs, "tt_read_status"))

	for _, want := range []string{"/tmp/fake.json", "stdio", "clusters", "maxBytes"} {
		if !strings.Contains(got, want) {
			t.Errorf("status = %q, want it to mention %q", got, want)
		}
	}
}

func TestStatusNamesEveryClusterAndItsReadOnly(t *testing.T) {
	cs := twoClusterSession(t)

	got := resultText(t, callTool(t, cs, "tt_read_status"))

	for _, want := range []string{"|environment|cluster|readOnly|", "|dev|main|false|", "|prod|analytics|true|"} {
		if !strings.Contains(got, want) {
			t.Errorf("status = %q, want it to carry %q", got, want)
		}
	}
}

// The status tool is core knowledge, so a source registering its own would
// silently replace what all six servers must answer the same way.
func TestSourceMayNotRegisterTheStatusTool(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a source shadowed the core's status tool without a word")
		}
	}()

	spec := testSpec()
	spec.Source = &statusStealingSource{}
	Build(spec, NewRuntime(testInventory(), nil, testProcess(), nil))
}

type statusStealingSource struct{ fakeSource }

func (*statusStealingSource) Tools(r *Registry[testConfig]) {
	Read(r, statusAction, "mine now", func(context.Context, testConfig, testIn) ([]block.Block, error) {
		return nil, nil
	})
}

// A nil logger must not fall back to slog.Default: that writes to stdout, where
// the stdio transport lives.
func TestRuntimeWithoutALoggerDiscards(t *testing.T) {
	rt := Runtime[testConfig]{}

	if rt.logger() == slog.Default() {
		t.Fatal("a nil logger fell back to the default one, which writes to stdout")
	}
}

func TestAFailedCallIsLoggedWithItsCause(t *testing.T) {
	var out strings.Builder
	log := slog.New(slog.NewTextHandler(&out, nil))
	spec := testSpec()
	spec.Source = &failingSource{}
	cs := connect(t, Build(spec, NewRuntime(testInventory(), nil, testProcess(), log)))

	callTool(t, cs, "tt_read_rows")

	got := out.String()
	if !strings.Contains(got, "the source said no") {
		t.Errorf("log = %q, want the cause the model never sees", got)
	}
}

type failingSource struct{ fakeSource }

func (*failingSource) Tools(r *Registry[testConfig]) {
	Read(r, "rows", "fail", func(context.Context, testConfig, testIn) ([]block.Block, error) {
		return nil, errors.New("the source said no")
	})
}

// A degraded start would otherwise repeat its reason on every call, having
// already said it once at startup.
func TestADegradedCallIsNotLoggedAsAnError(t *testing.T) {
	var out strings.Builder
	log := slog.New(slog.NewTextHandler(&out, nil))
	spec := testSpec()
	cs := connect(t, Build(spec, NewRuntime(testInventory(), errors.New("no config"), testProcess(), log)))

	callTool(t, cs, "tt_read_rows")

	if strings.Contains(out.String(), "level=ERROR") {
		t.Errorf("log = %q, want no error line", out.String())
	}
}

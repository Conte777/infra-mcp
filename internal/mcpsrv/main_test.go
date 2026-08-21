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

func testEnv() Env {
	return Env{Source: "fake", Profile: DefaultProfile, ConfigPath: "/tmp/fake.default.json", Transport: "stdio"}
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
	return connect(t, Build(spec, NewRuntime(spec.Defaults, degraded, testEnv(), nil)))
}

// The status tool takes no arguments, every other fake tool takes testIn.
func callTool(t *testing.T, cs *mcp.ClientSession, name string) *mcp.CallToolResult {
	t.Helper()
	var args map[string]any
	if !strings.HasSuffix(name, "_"+statusAction) {
		args = map[string]any{"name": "world"}
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

	if got != (&fakeSource{}).Instructions() {
		t.Fatalf("instructions = %q, want the source's", got)
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

	for _, want := range []string{"/tmp/fake.default.json", "stdio", DefaultProfile, "maxBytes"} {
		if !strings.Contains(got, want) {
			t.Errorf("status = %q, want it to mention %q", got, want)
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
	Build(spec, NewRuntime(spec.Defaults, nil, testEnv(), nil))
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
	cs := connect(t, Build(spec, NewRuntime(spec.Defaults, nil, testEnv(), log)))

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
	cs := connect(t, Build(spec, NewRuntime(spec.Defaults, errors.New("no config"), testEnv(), log)))

	callTool(t, cs, "tt_read_rows")

	if strings.Contains(out.String(), "level=ERROR") {
		t.Errorf("log = %q, want no error line", out.String())
	}
}

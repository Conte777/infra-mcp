// Package srvtest is the one set of checks every infra-mcp server runs against
// itself: the core's promises that a source could quietly break.
package srvtest

import (
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

const degradedReason = "conformance run: no config"

// Conformance runs every check the core's contract implies over spec, from each
// of the six source packages. Only the status tool is ever called on a healthy
// server: a conformance run must not need a live source.
func Conformance[C any, P mcpsrv.ConfigPtr[C]](t *testing.T, spec mcpsrv.Spec[C]) {
	t.Helper()

	proc := mcpsrv.Process{Source: spec.Name, Transport: "stdio"}
	inv := mcpsrv.Inventory[C]{Global: spec.Defaults}
	rt := mcpsrv.NewRuntime[C, P](inv, nil, proc, nil)
	degradedRT := mcpsrv.NewRuntime[C, P](inv, errors.New(degradedReason), proc, nil)

	prefix := spec.Source.Prefix()
	statusName := prefix + "_read_status"

	healthy := connect(t, mcpsrv.Build(spec, rt))
	degraded := connect(t, mcpsrv.Build(spec, degradedRT))

	tools := listTools(t, healthy)

	t.Run("names are assembled from the prefix and the access", func(t *testing.T) {
		want := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `_(read|write)_[a-z][a-z0-9_]*$`)
		for _, tool := range tools {
			if !want.MatchString(tool.Name) {
				t.Errorf("tool %q is outside the scheme the permissions allow-list globs over", tool.Name)
			}
		}
	})

	t.Run("access decides the hint and the marker", func(t *testing.T) {
		for _, tool := range tools {
			read := strings.HasPrefix(tool.Name, prefix+"_read_")
			if tool.Annotations == nil {
				t.Errorf("tool %q carries no annotations", tool.Name)
				continue
			}
			if tool.Annotations.ReadOnlyHint != read {
				t.Errorf("tool %q has ReadOnlyHint=%v, which contradicts its name", tool.Name, tool.Annotations.ReadOnlyHint)
			}
			_, marked := tool.Meta[metaRequiresUserInteraction]
			if read && marked {
				t.Errorf("read tool %q asks for confirmation, so it will never be auto-approved", tool.Name)
			}
			if !read && marked != rt.Settings.Write.RequireConfirmation {
				t.Errorf("write tool %q: confirmation marker = %v, want %v",
					tool.Name, marked, rt.Settings.Write.RequireConfirmation)
			}
		}
	})

	t.Run("every source tool names a cluster", func(t *testing.T) {
		for _, tool := range tools {
			if tool.Name == statusName {
				continue // the core's own, and it answers about the whole server
			}
			for _, arg := range []string{"environment", "cluster"} {
				if !slices.Contains(argumentNames(tool.InputSchema), arg) {
					t.Errorf("tool %q takes no %s argument; the core declares it for every tool", tool.Name, arg)
				}
			}
		}
	})

	t.Run("the core registers the status tool", func(t *testing.T) {
		for _, tool := range tools {
			if tool.Name == statusName {
				return
			}
		}
		t.Errorf("%s is missing from the tool set", statusName)
	})

	t.Run("an answer carries no structuredContent", func(t *testing.T) {
		res := call(t, healthy, statusName)
		if res.IsError {
			t.Fatalf("%s failed: %s", statusName, text(res))
		}
		if res.StructuredContent != nil {
			t.Errorf("structuredContent = %v; the answer is markdown and nothing else", res.StructuredContent)
		}
	})

	t.Run("a degraded start keeps the whole tool set", func(t *testing.T) {
		got, want := names(listTools(t, degraded)), names(tools)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("degraded tools = %v, want %v; the allow-list must not depend on the config", got, want)
		}
	})

	t.Run("a degraded start answers every call with the diagnosis", func(t *testing.T) {
		for _, tool := range listTools(t, degraded) {
			// A tool with required arguments is rejected by schema validation
			// before the core ever answers, which would test the SDK instead.
			if requiresArguments(tool.InputSchema) {
				continue
			}
			res := call(t, degraded, tool.Name)
			if !res.IsError {
				t.Errorf("%s answered normally on a degraded start", tool.Name)
				continue
			}
			if !strings.Contains(text(res), degradedReason) {
				t.Errorf("%s answered %q, want the config diagnosis", tool.Name, text(res))
			}
		}
	})
}

// Repeated rather than read off the core: conformance checks the wire, and a
// shared constant would let both sides drift together.
const metaRequiresUserInteraction = "anthropic/requiresUserInteraction"

// requiresArguments reads the wire form of an input schema, which arrives as a
// decoded JSON object rather than a *jsonschema.Schema.
func requiresArguments(schema any) bool {
	obj, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	req, ok := obj["required"].([]any)
	return ok && len(req) > 0
}

func argumentNames(schema any) []string {
	obj, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	props, ok := obj["properties"].(map[string]any)
	if !ok {
		return nil
	}
	return slices.Collect(maps.Keys(props))
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("server.Connect() = %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "srvtest", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client.Connect() = %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listTools(t *testing.T, cs *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list = %v", err)
	}
	return res.Tools
}

func call(t *testing.T, cs *mcp.ClientSession, name string) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name})
	if err != nil {
		t.Fatalf("tools/call %s = %v", name, err)
	}
	return res
}

func names(tools []*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

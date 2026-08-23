package mcpsrv

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Source is everything the core needs from a source — four methods and no
// more. Flags, config, transports, tool naming, budgets and rendering all stay
// with the core, so that a sixth source is a package under internal/source and
// nothing else.
type Source[C any] interface {
	// Prefix starts every tool name of this source ("pg").
	Prefix() string
	// Instructions is the text the model is handed once, at initialize.
	Instructions() string
	// Tools declares the tool set. It runs once at startup and may not depend
	// on the config having loaded: a degraded start registers the full set.
	Tools(r *Registry[C])
	// Close releases whatever the source opened.
	Close() error
}

// Runtime is what the core knows by the time tools are registered.
type Runtime[C any] struct {
	// Inventory is every cluster the config declares; a call is handed the one
	// its address names.
	Inventory Inventory[C]
	Settings  Settings
	// Degraded is the config failure a degraded start answers every call with.
	// While it is set no handler runs, so Inventory is never read.
	Degraded error
	// Process is what the status tool reports.
	Process Process
	// Logger writes to stderr. Nil discards: slog.Default writes to stdout,
	// where the stdio transport lives and a stray line breaks the session.
	Logger *slog.Logger
}

var discardLogger = slog.New(slog.DiscardHandler)

func (rt Runtime[C]) logger() *slog.Logger {
	if rt.Logger == nil {
		return discardLogger
	}
	return rt.Logger
}

// Registry is the only route into the tool set, and it has exactly two doors.
// Which one a tool came through settles its name, its annotations and what the
// source hands its handler — so "a write tool wearing a read name" is not
// something a source can express.
type Registry[C any] struct {
	server *mcp.Server
	prefix string
	rt     Runtime[C]
	tools  []*mcp.Tool
}

// NewRegistry prepares registration of prefix's tools on server.
func NewRegistry[C any](server *mcp.Server, prefix string, rt Runtime[C]) *Registry[C] {
	return &Registry[C]{server: server, prefix: prefix, rt: rt}
}

// Registered lists the tools declared so far, in registration order.
func (r *Registry[C]) Registered() []*mcp.Tool { return r.tools }

// Runtime is what the core already knows while tools are being declared: the
// config every call will be handed, whether the start is degraded, and a logger
// that is never nil. A source reads it to build what it opens lazily; nothing in
// it changes afterwards.
func (r *Registry[C]) Runtime() Runtime[C] {
	rt := r.rt
	rt.Logger = r.rt.logger()
	return rt
}

// Handler answers one tool call. It returns blocks; what they are rendered
// into is not its business.
type Handler[C, In any] func(ctx context.Context, cfg C, in In) ([]block.Block, error)

// Read registers a tool that only reads: named <prefix>_read_<action>, marked
// ReadOnlyHint, and never carrying the confirmation marker. The name is what a
// permissions allow-list globs over, which is why the core assembles it.
func Read[C any, In Addressed](r *Registry[C], action, description string, h Handler[C, In]) {
	register(r, accessRead, action, description, h)
}

// Write registers a tool that changes the source: named <prefix>_write_<action>,
// carrying the confirmation marker unless tools.write.requireConfirmation is off.
func Write[C any, In Addressed](r *Registry[C], action, description string, h Handler[C, In]) {
	register(r, accessWrite, action, description, h)
}

type access string

const (
	accessRead  access = "read"
	accessWrite access = "write"
)

// The marker that makes a client ask before the call. It holds even under
// bypassPermissions, so a write tool wears it rather than trusting a rule.
const metaRequiresUserInteraction = "anthropic/requiresUserInteraction"

// An action is one word of a tool name: anything else would break the scheme
// the allow-list globs over.
var actionRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func register[C, In any](r *Registry[C], a access, action, description string, h Handler[C, In]) {
	if !actionRE.MatchString(action) {
		panic(fmt.Sprintf("mcpsrv: %s tool action %q is not [a-z][a-z0-9_]*", a, action))
	}

	tool := &mcp.Tool{
		Name:        r.prefix + "_" + string(a) + "_" + action,
		Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: a == accessRead},
	}
	if a == accessWrite && r.rt.Settings.Write.RequireConfirmation {
		tool.Meta = mcp.Meta{metaRequiresUserInteraction: true}
	}
	r.tools = append(r.tools, tool)

	budget := r.rt.Settings.Output.Budget()
	// Out is any and the returned value is always nil: anything else fills
	// structuredContent, and the answer stops being the markdown we shaped.
	mcp.AddTool(r.server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		blocks, err := call(ctx, r, a, h, in)
		if err != nil {
			f := asFailure(err)
			// The cause and nothing past it: a source value or a password
			// reaching the log is the one thing this line must not do. A
			// degraded start logs at debug — its reason went out once at
			// startup, and every call after would repeat it.
			level := slog.LevelError
			if r.rt.Degraded != nil {
				level = slog.LevelDebug
			}
			r.rt.logger().Log(ctx, level, "tool call failed", "tool", tool.Name, "kind", f.Kind.String(), "error", err)
			res := textResult(block.Markdown(f.blocks(), budget))
			res.SetError(f) // keeps the rendered content, sets isError
			return res, nil, nil
		}
		return textResult(block.Markdown(blocks, budget)), nil, nil
	})
}

// call resolves the address before the handler sees anything: which cluster a
// call reaches, and whether that cluster takes a write at all, are the core's
// to decide. A degraded start stops being a special case here too — it is the
// not-configured kind, taking the route every other failure takes.
func call[C, In any](ctx context.Context, r *Registry[C], a access, h Handler[C, In], in In) ([]block.Block, error) {
	if r.rt.Degraded != nil {
		return nil, &Failure{
			Kind:   KindNotConfigured,
			Detail: r.rt.Degraded.Error(),
			Err:    r.rt.Degraded,
		}
	}

	cfg := r.rt.Inventory.Global
	if addressed, ok := any(in).(Addressed); ok {
		cluster, err := r.rt.Inventory.Find(addressed.address())
		if err != nil {
			return nil, err
		}
		// The tool is not hidden: it is one tool for every address, and only
		// the address decides. Hiding it would make the tool set depend on the
		// config, which the allow-list may not.
		if a == accessWrite && cluster.ReadOnly {
			return nil, &Failure{
				Kind:   KindDenied,
				Detail: fmt.Sprintf("%s is marked readOnly in the config", cluster.Address),
				Hint:   "a read tool works at this address; writing needs the readOnly key cleared",
			}
		}
		cfg = cluster.Config
	}
	return h(ctx, cfg, in)
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

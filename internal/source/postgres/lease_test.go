package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

func TestResolveDatabase(t *testing.T) {
	tests := []struct {
		name    string
		include []string
		exclude []string
		arg     string
		named   bool
		want    string
		kind    mcpsrv.Kind
	}{
		{name: "a tool that names none runs in the entry point", want: "app_db"},
		{
			name: "an empty include is the whole cluster",
			arg:  "other", named: true, want: "other",
		},
		{
			name: "include is a white list", include: []string{"orders", "app_*"},
			arg: "orders", named: true, want: "orders",
		},
		{
			name: "what include does not name is off limits", include: []string{"orders"},
			arg: "other", named: true, kind: mcpsrv.KindDenied,
		},
		{
			// The entry point is where list_databases connects, and that is all:
			// include naming one database must not quietly yield two.
			name: "include does not spare the entry point", include: []string{"orders"},
			arg: "app_db", named: true, kind: mcpsrv.KindDenied,
		},
		{
			name: "exclude is subtracted after include", include: []string{"tmp_*"},
			exclude: []string{"tmp_1"}, arg: "tmp_1", named: true, kind: mcpsrv.KindDenied,
		},
		{name: "exclude hides", exclude: []string{"tmp_*"}, arg: "tmp_1", named: true, kind: mcpsrv.KindDenied},
		{
			// A quoted identifier may hold a slash, and path.Match reads one as
			// a path separator that * refuses to cross.
			name: "exclude hides a name with a slash in it", exclude: []string{"tmp_*"},
			arg: "tmp_a/b", named: true, kind: mcpsrv.KindDenied,
		},
		{
			name: "include reaches a name with a slash in it", include: []string{"rep_*"},
			arg: "rep_a/b", named: true, want: "rep_a/b",
		},
		{name: "exclude misses", exclude: []string{"tmp_*"}, arg: "other", named: true, want: "other"},
		{
			// The schema marks the argument required, which says nothing about
			// it being non-empty — and an empty dbname makes libpq read the
			// role name as one.
			name: "a tool that names an empty database", arg: "", named: true, kind: mcpsrv.KindBadArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Databases = Databases{Default: "app_db", Include: tt.include, Exclude: tt.exclude}

			got, err := resolveDatabase(cfg, tt.arg, tt.named)
			// A resolve that succeeds always names a database, so an empty want
			// is the failing case; kind says how it fails.
			if tt.want == "" {
				var f *mcpsrv.Failure
				if !errors.As(err, &f) {
					t.Fatalf("resolveDatabase(%q) = %q, %v; want a failure", tt.arg, got, err)
				}
				if f.Kind != tt.kind {
					t.Errorf("kind = %v, want %v", f.Kind, tt.kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveDatabase(%q): %v", tt.arg, err)
			}
			if got != tt.want {
				t.Errorf("resolveDatabase(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

func TestMillis(t *testing.T) {
	if got := millis(mcpsrv.Duration(30 * time.Second)); got != "30000" {
		t.Errorf("millis(30s) = %q, want 30000", got)
	}
	if got := millis(0); got != "0" {
		t.Errorf("millis(0) = %q, want 0 — no limit", got)
	}
}

type leaseArgs struct {
	Args

	Table string `json:"table"`
}

// testInventory is the one cluster the fixtures below address.
func testInventory() mcpsrv.Inventory[Config] {
	return mcpsrv.Inventory[Config]{
		Global:   Defaults(),
		Clusters: []mcpsrv.Cluster[Config]{{Address: testCluster, Config: Defaults()}},
	}
}

// The wrappers are the only route into the registry, and the door taken settles
// the name and the annotations — the source never spells either out.
func TestWrappersNameAndAnnotateByDoor(t *testing.T) {
	s := &Source{}
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	rt := mcpsrv.NewRuntime[Config, *Config](testInventory(), nil, mcpsrv.Process{}, nil)
	r := mcpsrv.NewRegistry(server, Prefix, rt)

	nothing := func(context.Context, pgx.Tx, Config, leaseArgs) ([]block.Block, error) { return nil, nil }
	read(s, r, "peek", "read something", nothing)
	write(s, r, "poke", "change something", nothing)

	tools := r.Registered()
	if len(tools) != 2 {
		t.Fatalf("registered %d tools, want 2", len(tools))
	}

	if got := tools[0].Name; got != "pg_read_peek" {
		t.Errorf("read tool name = %q, want pg_read_peek", got)
	}
	if !tools[0].Annotations.ReadOnlyHint {
		t.Error("a read tool must carry ReadOnlyHint")
	}
	if tools[0].Meta != nil {
		t.Errorf("a read tool must not ask for confirmation, got %v", tools[0].Meta)
	}

	if got := tools[1].Name; got != "pg_write_poke" {
		t.Errorf("write tool name = %q, want pg_write_poke", got)
	}
	if tools[1].Annotations.ReadOnlyHint {
		t.Error("a write tool must not carry ReadOnlyHint")
	}
	if tools[1].Meta["anthropic/requiresUserInteraction"] != true {
		t.Errorf("a write tool must ask first, got %v", tools[1].Meta)
	}
}

// A pool that never reached the server holds a cache slot for nothing, and a
// model walking wrong database names would push the working one out.
func TestLeaseForgetsAPoolThatNeverConnected(t *testing.T) {
	p, cfg := testPools(t, 4, time.Minute)
	s := &Source{pools: p}

	_, err := inTx(s, pgx.ReadOnly, func(context.Context, pgx.Tx, Config, ConnectionArgs) ([]block.Block, error) {
		t.Fatal("the handler must not run without a connection")
		return nil, nil
	})(context.Background(), cfg, ConnectionArgs{Address: testCluster})

	var f *mcpsrv.Failure
	if !errors.As(err, &f) || f.Kind != mcpsrv.KindUnavailable {
		t.Fatalf("err = %v, want an unavailable failure", err)
	}
	if open := p.open(); open != 0 {
		t.Errorf("%d pools cached after a failed connection, want none", open)
	}
}

// Build is exported and says nothing about being called once; a second build
// must not leave the first cache running with nobody holding it.
func TestToolsReplacesAnEarlierPoolCache(t *testing.T) {
	s := &Source{}
	build := func() {
		server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
		rt := mcpsrv.NewRuntime[Config, *Config](testInventory(), nil, mcpsrv.Process{}, nil)
		s.Tools(mcpsrv.NewRegistry(server, Prefix, rt))
	}

	build()
	first := s.pools
	build()

	if s.pools == first {
		t.Fatal("the second build kept the first cache")
	}
	if _, _, err := first.acquire(context.Background(), Defaults(), at("app_db")); !errors.Is(err, errPoolsClosed) {
		t.Errorf("the first cache is still open: %v", err)
	}
}

// A handler never runs without a pool, and a server that got this far without
// one has a config problem to report rather than a nil to dereference.
func TestHandlerWithoutPools(t *testing.T) {
	h := inTx(&Source{}, pgx.ReadOnly, func(context.Context, pgx.Tx, Config, leaseArgs) ([]block.Block, error) {
		t.Fatal("the handler must not run")
		return nil, nil
	})

	_, err := h(context.Background(), Defaults(), leaseArgs{Args: Args{Address: testCluster}})

	var f *mcpsrv.Failure
	if !errors.As(err, &f) || f.Kind != mcpsrv.KindNotConfigured {
		t.Errorf("err = %v, want a not-configured failure", err)
	}
}

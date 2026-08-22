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
		showAll bool
		exclude []string
		arg     string
		want    string
		denied  bool
	}{
		{name: "no argument is the default", arg: "", want: "app_db"},
		{name: "the default itself", arg: "app_db", want: "app_db"},
		{name: "another database is off limits", arg: "other", denied: true},
		{name: "showAll opens the cluster", showAll: true, arg: "other", want: "other"},
		{name: "exclude still hides", showAll: true, exclude: []string{"tmp_*"}, arg: "tmp_1", denied: true},
		{name: "exclude misses", showAll: true, exclude: []string{"tmp_*"}, arg: "other", want: "other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Databases = Databases{Default: "app_db", ShowAll: tt.showAll, Exclude: tt.exclude}

			got, err := resolveDatabase(cfg, tt.arg)
			if tt.denied {
				var f *mcpsrv.Failure
				if !errors.As(err, &f) {
					t.Fatalf("resolveDatabase(%q) = %q, %v; want a failure", tt.arg, got, err)
				}
				if f.Kind != mcpsrv.KindDenied {
					t.Errorf("kind = %v, want denied", f.Kind)
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

// The wrappers are the only route into the registry, and the door taken settles
// the name and the annotations — the source never spells either out.
func TestWrappersNameAndAnnotateByDoor(t *testing.T) {
	s := &Source{}
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	rt := mcpsrv.NewRuntime[Config, *Config](Defaults(), nil, mcpsrv.Env{}, nil)
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
	p := testPools(t, 4, time.Minute)
	s := &Source{pools: p}

	_, err := inTx(s, pgx.ReadOnly, func(context.Context, pgx.Tx, Config, ConnectionArgs) ([]block.Block, error) {
		t.Fatal("the handler must not run without a connection")
		return nil, nil
	})(context.Background(), p.cfg, ConnectionArgs{})

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
		rt := mcpsrv.NewRuntime[Config, *Config](Defaults(), nil, mcpsrv.Env{}, nil)
		s.Tools(mcpsrv.NewRegistry(server, Prefix, rt))
	}

	build()
	first := s.pools
	build()

	if s.pools == first {
		t.Fatal("the second build kept the first cache")
	}
	if _, _, err := first.acquire(context.Background(), "app_db"); !errors.Is(err, errPoolsClosed) {
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

	_, err := h(context.Background(), Defaults(), leaseArgs{})

	var f *mcpsrv.Failure
	if !errors.As(err, &f) || f.Kind != mcpsrv.KindNotConfigured {
		t.Errorf("err = %v, want a not-configured failure", err)
	}
}

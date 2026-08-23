package postgres

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

// Prefix starts every tool name of this source.
const Prefix = "pg"

// Source is the postgres half of the server: what the core does not own.
type Source struct {
	// pools is built when tools are declared and stays nil on a degraded start,
	// where no handler ever runs.
	pools *pools
}

// Spec is the whole postgres server, ready for [mcpsrv.Main].
func Spec() mcpsrv.Spec[Config] {
	return mcpsrv.Spec[Config]{
		Name:     Name,
		Source:   &Source{},
		Defaults: Defaults(),
		Minimal:  Minimal(),
		Types:    ConfigTypes(),
	}
}

// Prefix implements [mcpsrv.Source].
func (*Source) Prefix() string { return Prefix }

// Instructions implements [mcpsrv.Source].
func (*Source) Instructions() string { return instructions }

// Tools implements [mcpsrv.Source]. Tools are declared either way — a degraded
// start keeps the full set (ADR-0001) — but nothing is opened: on a degraded
// start the config is the defaults and no handler will run.
func (s *Source) Tools(r *mcpsrv.Registry[Config]) {
	rt := r.Runtime()

	// Above the guard: below it a degraded start would lose the tools, and an
	// allow-list globbing pg_read_* would depend on the server having a config.
	registerTools(s, r)

	if rt.Degraded != nil {
		return
	}

	// Building twice is not this source's business to police, but leaking the
	// first cache — health-check goroutines and all — would be.
	if s.pools != nil {
		s.pools.Close()
	}
	s.pools = newPools(rt.Logger)
	for _, cluster := range rt.Inventory.Clusters {
		if cluster.Config.Pool.EagerInit {
			s.warm(cluster, rt.Logger)
		}
	}
}

// warm opens the default database's pool of one cluster at startup instead of
// on the first call. In the background, and one goroutine per cluster:
// eagerInit is about a server deploy seeing a bad connection early, not about
// refusing to start — and a prod cluster that is unreachable must not hold up
// the dev one behind it.
func (s *Source) warm(cluster mcpsrv.Cluster[Config], log *slog.Logger) {
	go func() {
		cfg := cluster.Config
		ctx, cancel := context.WithTimeout(context.Background(), cfg.ClientDeadline())
		defer cancel()

		addr := address{cluster: cluster.Address, database: cfg.Databases.Default}
		pool, release, err := s.pools.acquire(ctx, cfg, addr)
		if err == nil {
			defer release()
			err = pool.Ping(ctx)
		}
		if errors.Is(err, errPoolsClosed) {
			log.Debug("eager init stopped by shutdown", "address", addr)
			return
		}
		if err != nil {
			log.Error("eager init failed", "address", addr, "error", err)
			return
		}
		log.Info("connected", "address", addr)
	}()
}

// Close implements [mcpsrv.Source].
func (s *Source) Close() error {
	if s.pools != nil {
		s.pools.Close()
	}
	return nil
}

// instructions go out once, at initialize — before any connection exists, so
// nothing here can be a live list of databases.
const instructions = `This server reaches the postgres clusters of every environment its config
declares, configured ahead of time.

Tool names read <prefix>_<read|write>_<action>: a pg_read_ tool never changes
anything, a pg_write_ tool always asks first. Where a tool call lands is an
argument of that tool, not a property of this server: environment, cluster and
database, all three required and none of them defaulted.

pg_read_status reports which config file is loaded and which clusters it
serves; when the server is not configured, every tool answers with the reason
instead.`

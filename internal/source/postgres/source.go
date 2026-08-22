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

	// Tools go here, above the guard: below it a degraded start would lose them,
	// and an allow-list globbing pg_read_* would depend on the server having a
	// config.

	if rt.Degraded != nil {
		return
	}

	// Building twice is not this source's business to police, but leaking the
	// first cache — health-check goroutines and all — would be.
	if s.pools != nil {
		s.pools.Close()
	}
	s.pools = newPools(rt.Config, rt.Logger)
	if rt.Config.Pool.EagerInit {
		s.warm(rt.Config, rt.Logger)
	}
}

// warm opens the default database's pool at startup instead of on the first
// call. In the background: eagerInit is about a server deploy seeing a bad
// connection early, not about refusing to start.
func (s *Source) warm(cfg Config, log *slog.Logger) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.ClientDeadline())
		defer cancel()

		db := cfg.Databases.Default
		pool, release, err := s.pools.acquire(ctx, db)
		if err == nil {
			defer release()
			err = pool.Ping(ctx)
		}
		if errors.Is(err, errPoolsClosed) {
			log.Debug("eager init stopped by shutdown", "database", db)
			return
		}
		if err != nil {
			log.Error("eager init failed", "database", db, "error", err)
			return
		}
		log.Info("connected", "database", db)
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
const instructions = `This server reaches one postgres server, configured ahead of time.

Tool names read <prefix>_<read|write>_<action>: a pg_read_ tool never changes
anything, a pg_write_ tool always asks first. Which database a tool talks to is
an argument of that tool, not a property of this server.

pg_read_status reports which config file is loaded; when the server is not
configured, every tool answers with the reason instead.`

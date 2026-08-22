package postgres

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Input is what every tool's argument struct satisfies. The wrapper resolves
// the database before the handler runs, so which database a call reaches is
// never the handler's decision.
type Input interface{ database() string }

// Args is the database argument every tool carries (ADR-0001). The field is
// required: with no omitzero the core's schema marks it so.
type Args struct {
	Database string `json:"database" jsonschema:"database to run in"`
}

func (a Args) database() string { return a.Database }

// ConnectionArgs is for a tool that asks about the connection as a whole rather
// than about one database; it runs in databases.default.
type ConnectionArgs struct{}

func (ConnectionArgs) database() string { return "" }

// TxFunc is a tool handler. The transaction is already open, already limited by
// SET LOCAL and, for a read tool, already READ ONLY — and there is no way from
// here to reach the pool it came from.
type TxFunc[In any] func(ctx context.Context, tx pgx.Tx, cfg Config, in In) ([]block.Block, error)

// read registers a read tool: it runs inside BEGIN TRANSACTION READ ONLY, which
// catches a DELETE inside a CTE or inside a called function the way no parser
// does (ADR-0001).
func read[In Input](s *Source, r *mcpsrv.Registry[Config], action, description string, h TxFunc[In]) {
	mcpsrv.Read(r, action, description, inTx(s, pgx.ReadOnly, h))
}

// write registers a write tool: read-write, committed only if the handler
// returns no error. The transaction is also the only place SET LOCAL holds, so
// without it a write would have no server-side limit at all.
func write[In Input](s *Source, r *mcpsrv.Registry[Config], action, description string, h TxFunc[In]) {
	mcpsrv.Write(r, action, description, inTx(s, pgx.ReadWrite, h))
}

// rollbackGrace bounds the rollback of a call whose own deadline has expired:
// without a live context the driver drops the connection instead.
const rollbackGrace = 2 * time.Second

func inTx[In Input](s *Source, mode pgx.TxAccessMode, h TxFunc[In]) mcpsrv.Handler[Config, In] {
	return func(ctx context.Context, cfg Config, in In) ([]block.Block, error) {
		if s.pools == nil {
			return nil, &mcpsrv.Failure{
				Kind:   mcpsrv.KindNotConfigured,
				Detail: "the server started without a usable config",
			}
		}

		db, err := resolveDatabase(cfg, in.database())
		if err != nil {
			return nil, err
		}

		ctx, cancel := context.WithTimeout(ctx, cfg.ClientDeadline())
		defer cancel()

		pool, release, err := s.pools.acquire(ctx, db)
		if err != nil {
			return nil, failure(ctx, cfg, err)
		}
		defer release()

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: mode})
		if err != nil {
			return nil, failure(ctx, cfg, err)
		}
		defer func() {
			closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackGrace)
			defer closeCancel()
			_ = tx.Rollback(closeCtx) // after a commit this is ErrTxClosed
		}()

		if err := setLocalTimeouts(ctx, tx, cfg.Timeouts); err != nil {
			return nil, failure(ctx, cfg, err)
		}

		blocks, err := h(ctx, tx, cfg, in)
		if err != nil {
			return nil, failure(ctx, cfg, err)
		}
		if mode == pgx.ReadWrite {
			if err := tx.Commit(ctx); err != nil {
				return nil, failure(ctx, cfg, err)
			}
		}
		return blocks, nil
	}
}

// setLocal is set_config rather than SET LOCAL so the values travel as
// parameters; a limit is an integer from the config, but building SQL out of
// one is a habit worth not having.
const setLocal = `SELECT set_config('statement_timeout', $1, true), set_config('lock_timeout', $2, true)`

// setLocalTimeouts limits the transaction server-side. Only SET LOCAL survives
// the trip: as a pool-level startup parameter the limit is either refused by
// PgBouncer or silently dropped, and a server with no timeout looks exactly like
// one that has it (ADR-0001).
func setLocalTimeouts(ctx context.Context, tx pgx.Tx, t Timeouts) error {
	_, err := tx.Exec(ctx, setLocal, millis(t.Query), millis(t.Lock))
	return err
}

// millis renders a duration the way postgres reads a bare number in these
// settings. Zero means no limit, which is what a zeroed config key asks for.
func millis(d mcpsrv.Duration) string {
	return strconv.FormatInt(d.Duration().Milliseconds(), 10)
}

// resolveDatabase turns the database argument into the database to connect to.
// An empty argument is the configured default, so a tool about the connection
// as a whole needs no argument at all.
func resolveDatabase(cfg Config, name string) (string, error) {
	def := cfg.Databases.Default
	if name == "" || name == def {
		return def, nil
	}

	denied := func(detail, hint string) (string, error) {
		return "", &mcpsrv.Failure{Kind: mcpsrv.KindDenied, Detail: detail, Hint: hint}
	}

	if !cfg.Databases.ShowAll {
		return denied(
			fmt.Sprintf("this server is configured to reach only %q", def),
			"set databases.showAll in the config to reach the rest of the cluster")
	}
	for _, pat := range cfg.Databases.Exclude {
		// The patterns were checked when the config loaded, so a match error is
		// not reachable here.
		if ok, _ := path.Match(pat, name); ok {
			return denied(
				fmt.Sprintf("database %q is hidden by databases.exclude (%q)", name, pat),
				"")
		}
	}
	return name, nil
}

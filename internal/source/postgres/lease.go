package postgres

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Input is what every tool's argument struct satisfies. The wrapper resolves
// the address before the handler runs, so which cluster and which database a
// call reaches is never the handler's decision.
type Input interface {
	mcpsrv.Addressed
	// cluster is the same value the core reads through [mcpsrv.Addressed],
	// whose accessor is unexported to this package; it comes off the embedded
	// field instead.
	cluster() mcpsrv.Address
	// database is the database the call names, and whether it names one at all.
	// The second half is the tool's shape, not its arguments: a tool about the
	// cluster runs in the entry point, and no string a model sends can ask for
	// that.
	database() (string, bool)
}

// Args is what every tool carries: the cluster the core addresses, and the
// database inside it (ADR-0001). Neither is omitzero, so the core's schema
// marks all three required.
type Args struct {
	mcpsrv.Address

	Database string `json:"database" jsonschema:"database to run in"`
}

func (a Args) cluster() mcpsrv.Address { return a.Address }

func (a Args) database() (string, bool) { return a.Database, true }

// ConnectionArgs is for a tool that asks about a cluster as a whole rather than
// about one database; it runs in that cluster's databases.default, which is why
// that key is an entry point and not one more reachable database.
type ConnectionArgs struct {
	mcpsrv.Address
}

func (a ConnectionArgs) cluster() mcpsrv.Address { return a.Address }

func (ConnectionArgs) database() (string, bool) { return "", false }

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

		name, named := in.database()
		db, err := resolveDatabase(cfg, name, named)
		if err != nil {
			return nil, err
		}
		addr := address{cluster: in.cluster(), database: db}

		ctx, cancel := context.WithTimeout(ctx, cfg.ClientDeadline())
		defer cancel()

		pool, release, err := s.pools.acquire(ctx, cfg, addr)
		if err != nil {
			return nil, failure(ctx, cfg, err)
		}
		defer release()

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: mode})
		if err != nil {
			// A pool that never reached the server is not worth a cache slot:
			// the driver dials again on the next call either way, and a wrong
			// database name would otherwise push a working pool out.
			var connErr *pgconn.ConnectError
			if errors.As(err, &connErr) {
				s.pools.forget(addr)
			}
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

// listHint is what a refused database argument is answered with: the tool that
// prints exactly the databases this address does reach.
const listHint = "pg_read_list_databases shows the databases this address reaches"

// resolveDatabase turns the database argument into the database to connect to.
// A tool that names none runs in databases.default: the catalog is not readable
// without being connected to something, and that is the only thing the lists do
// not decide. A named database is checked against them whether or not it is the
// default — an include naming one database must not quietly yield two.
func resolveDatabase(cfg Config, name string, named bool) (string, error) {
	if !named {
		return cfg.Databases.Default, nil
	}

	// The schema marks the argument required, which says nothing about it being
	// non-empty — and an empty dbname makes libpq read the role name as one.
	if name == "" {
		return "", &mcpsrv.Failure{
			Kind:   mcpsrv.KindBadArgument,
			Detail: "the database argument is empty",
			Hint:   listHint,
		}
	}

	denied := func(detail, hint string) (string, error) {
		return "", &mcpsrv.Failure{Kind: mcpsrv.KindDenied, Detail: detail, Hint: hint}
	}

	if inc := cfg.Databases.Include; len(inc) > 0 {
		if _, ok := matches(inc, name); !ok {
			return denied(
				fmt.Sprintf("database %q is not among the ones databases.include names", name),
				listHint)
		}
	}
	if pat, ok := matches(cfg.Databases.Exclude, name); ok {
		return denied(fmt.Sprintf("database %q is hidden by databases.exclude (%q)", name, pat), "")
	}
	return name, nil
}

// matches is the first pattern name matches. The patterns were checked when the
// config loaded, so a match error is not reachable here.
func matches(patterns []string, name string) (string, bool) {
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, name); ok {
			return pat, true
		}
	}
	return "", false
}

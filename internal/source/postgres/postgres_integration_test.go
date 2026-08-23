//go:build integration

package postgres

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
	"github.com/Conte777/infra-mcp/internal/pgtest"
)

// otherDatabase is what every postgres cluster has besides the one the harness
// creates; the eviction test needs a second database to walk into.
const otherDatabase = "postgres"

// callArgs is the address the harness's one cluster answers at; only the
// database argument varies between the calls below.
var callArgs = Args{Address: testCluster}

func testSource(t *testing.T, tune func(*Config)) (*Source, Config) {
	t.Helper()

	dsn := pgtest.Start(t)
	parsed, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the harness DSN: %v", err)
	}

	cfg := Defaults()
	cfg.Connection = Connection{
		Host:     parsed.Host,
		Port:     int(parsed.Port),
		User:     parsed.User,
		Password: parsed.Password,
		SSLMode:  SSLDisable,
	}
	cfg.Databases.Default = parsed.Database
	if tune != nil {
		tune(&cfg)
	}

	s := &Source{pools: newPools(slog.New(slog.DiscardHandler))}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s, cfg
}

// exec runs SQL outside the wrappers, for what a test needs before the tool
// does anything — including the statements no transaction may carry.
func exec(t *testing.T, cfg Config, database, sql string) {
	t.Helper()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connString(cfg.Connection, database))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func runRead[In Input](t *testing.T, s *Source, cfg Config, in In, h TxFunc[In]) ([]block.Block, error) {
	t.Helper()
	return inTx(s, pgx.ReadOnly, h)(context.Background(), cfg, in)
}

func runWrite[In Input](t *testing.T, s *Source, cfg Config, in In, h TxFunc[In]) ([]block.Block, error) {
	t.Helper()
	return inTx(s, pgx.ReadWrite, h)(context.Background(), cfg, in)
}

// wantKind asserts the category and hands back the two texts the model reads,
// so a caller that cares about them does not have to unwrap again.
func wantKind(t *testing.T, err error, want mcpsrv.Kind) (detail, hint string) {
	t.Helper()

	var f *mcpsrv.Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want a *mcpsrv.Failure", err)
	}
	if f.Kind != want {
		t.Fatalf("kind = %v (%v), want %v", f.Kind, f, want)
	}
	return f.Detail, f.Hint
}

// The transaction is the read-only boundary, not a parser: a DELETE hidden in a
// CTE is exactly what a parser waves through (ADR-0001).
func TestReadOnlyTransactionStopsAWriteInsideACTE(t *testing.T) {
	s, cfg := testSource(t, nil)
	exec(t, cfg, cfg.Databases.Default, `CREATE TABLE t (id int); INSERT INTO t VALUES (1), (2)`)

	_, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			var n int
			err := tx.QueryRow(ctx, `WITH gone AS (DELETE FROM t RETURNING id) SELECT count(*) FROM gone`).Scan(&n)
			return nil, err
		})

	wantKind(t, err, mcpsrv.KindDenied)

	var left int
	if _, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			return nil, tx.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&left)
		}); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 2 {
		t.Errorf("%d rows left, want 2 — the CTE deleted through a read-only transaction", left)
	}
}

// SET LOCAL is the only form of the limit that survives a pooler, so what the
// test proves is that the server cancels — not that a Go timer fired.
func TestStatementTimeoutFiresOnTheServer(t *testing.T) {
	s, cfg := testSource(t, func(c *Config) {
		c.Timeouts.Query = mcpsrv.Duration(200 * time.Millisecond)
	})

	start := time.Now()
	_, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			_, err := tx.Exec(ctx, `SELECT pg_sleep(5)`)
			return nil, err
		})

	detail, hint := wantKind(t, err, mcpsrv.KindTimeout)
	if elapsed := time.Since(start); elapsed > cfg.ClientDeadline() {
		t.Errorf("took %s: the client deadline fired, not the server", elapsed)
	}
	if detail == "" || hint == "" {
		t.Errorf("detail = %q, hint = %q; want the limit named and a way out", detail, hint)
	}
}

func TestLockTimeoutFiresOnTheServer(t *testing.T) {
	s, cfg := testSource(t, func(c *Config) {
		c.Timeouts.Lock = mcpsrv.Duration(200 * time.Millisecond)
		c.Timeouts.Query = mcpsrv.Duration(10 * time.Second)
	})
	exec(t, cfg, cfg.Databases.Default, `CREATE TABLE locked (id int)`)

	// A lock held from outside the wrapper, for as long as the test needs it.
	ctx := context.Background()
	blocker, err := pgx.Connect(ctx, connString(cfg.Connection, cfg.Databases.Default))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = blocker.Close(ctx) }()
	if _, err := blocker.Exec(ctx, `BEGIN; LOCK TABLE locked IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock: %v", err)
	}

	start := time.Now()
	_, err = runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			return nil, tx.QueryRow(ctx, `SELECT count(*) FROM locked`).Scan(new(int))
		})

	wantKind(t, err, mcpsrv.KindTimeout)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s: lock_timeout did not fire", elapsed)
	}
}

func TestWriteCommits(t *testing.T) {
	s, cfg := testSource(t, nil)
	exec(t, cfg, cfg.Databases.Default, `CREATE TABLE w (id int)`)

	if _, err := runWrite(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			_, err := tx.Exec(ctx, `INSERT INTO w VALUES (1)`)
			return nil, err
		}); err != nil {
		t.Fatalf("write: %v", err)
	}

	var rows int
	if _, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			return nil, tx.QueryRow(ctx, `SELECT count(*) FROM w`).Scan(&rows)
		}); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d rows, want the write committed", rows)
	}
}

// A write tool that fails halfway leaves nothing behind: the transaction the
// wrapper opened is what makes a multi-statement write all-or-nothing.
func TestWriteRollsBackOnFailure(t *testing.T) {
	s, cfg := testSource(t, nil)
	exec(t, cfg, cfg.Databases.Default, `CREATE TABLE w (id int)`)

	wantErr := errors.New("the tool gave up")
	_, err := runWrite(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			if _, err := tx.Exec(ctx, `INSERT INTO w VALUES (1)`); err != nil {
				return nil, err
			}
			return nil, wantErr
		})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the handler's own error", err)
	}

	var rows int
	if _, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			return nil, tx.QueryRow(ctx, `SELECT count(*) FROM w`).Scan(&rows)
		}); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d rows, want the failed write rolled back", rows)
	}
}

// Nothing is opened until a call needs it, and the pool for a database is
// opened once.
func TestPoolsOpenOnFirstCall(t *testing.T) {
	s, cfg := testSource(t, nil)

	if open := s.pools.open(); open != 0 {
		t.Fatalf("%d pools before the first call, want none", open)
	}
	for range 2 {
		if _, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
			func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
				return nil, tx.QueryRow(ctx, `SELECT 1`).Scan(new(int))
			}); err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	if open := s.pools.open(); open != 1 {
		t.Errorf("%d pools after two calls to one database, want 1", open)
	}
}

// Eviction is what keeps a walk through fifty databases from eating someone
// else's max_connections, and the refcount is what keeps it from doing that to
// a query in flight.
func TestEvictionLeavesARunningQueryAlone(t *testing.T) {
	s, cfg := testSource(t, func(c *Config) { c.Pool.MaxDatabases = 1; c.Databases.ShowAll = true })

	evicted := make(chan struct{})
	_, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			// Background on purpose: this is the concurrent call whose pool
			// eviction the running one must survive.
			go func() { //nolint:contextcheck // a second, independent call
				defer close(evicted)
				if _, err := runRead(t, s, cfg, Args{Address: testCluster, Database: otherDatabase},
					func(ctx context.Context, tx pgx.Tx, _ Config, _ Args) ([]block.Block, error) {
						return nil, tx.QueryRow(ctx, `SELECT 1`).Scan(new(int))
					}); err != nil {
					t.Errorf("second database: %v", err)
				}
			}()
			<-evicted

			var n int
			return nil, tx.QueryRow(ctx, `SELECT 1`).Scan(&n)
		})
	if err != nil {
		t.Fatalf("the query outlived its pool's eviction badly: %v", err)
	}
}

func TestUnknownDatabaseIsABadArgument(t *testing.T) {
	s, cfg := testSource(t, func(c *Config) { c.Databases.ShowAll = true })

	_, err := runRead(t, s, cfg, Args{Address: testCluster, Database: "no_such_database"},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ Args) ([]block.Block, error) {
			return nil, tx.QueryRow(ctx, `SELECT 1`).Scan(new(int))
		})

	wantKind(t, err, mcpsrv.KindBadArgument)
}

// The rejection arrives as a PgError wrapped in a failed connection attempt,
// and "unavailable" would send the operator looking at the wrong thing.
func TestBadPasswordIsDenied(t *testing.T) {
	s, cfg := testSource(t, func(c *Config) { c.Connection.Password = "not-the-password" })

	_, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			return nil, tx.QueryRow(ctx, `SELECT 1`).Scan(new(int))
		})

	wantKind(t, err, mcpsrv.KindDenied)
}

func TestUnreachableServerIsUnavailable(t *testing.T) {
	s, cfg := testSource(t, func(c *Config) {
		c.Connection.Port = 1
		c.Timeouts.Connect = mcpsrv.Duration(time.Second)
	})

	_, err := runRead(t, s, cfg, ConnectionArgs{Address: testCluster},
		func(ctx context.Context, tx pgx.Tx, _ Config, _ ConnectionArgs) ([]block.Block, error) {
			return nil, tx.QueryRow(ctx, `SELECT 1`).Scan(new(int))
		})

	detail, _ := wantKind(t, err, mcpsrv.KindUnavailable)
	if detail == "" {
		t.Error("an unreachable server must say where we tried")
	}
}

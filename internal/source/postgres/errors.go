package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

// The SQLSTATEs worth naming one by one; everything else is judged by its class.
const (
	codeQueryCanceled       = "57014"
	codeLockNotAvailable    = "55P03"
	codeReadOnlyTransaction = "25006"
	codeInvalidCatalogName  = "3D000"
	codeInsufficientPrivs   = "42501"
	codeProtocolViolation   = "08P01"
)

// failure turns whatever came back from the driver into the one shape the model
// sees. Unclassified errors keep no text: a stray SQLSTATE reaching the model is
// what the core's Failure exists to prevent (ADR-0003).
func failure(ctx context.Context, cfg Config, err error) error {
	if err == nil {
		return nil
	}

	var already *mcpsrv.Failure
	if errors.As(err, &already) {
		return already
	}

	// Before ConnectError: a refused database name or a rejected password comes
	// back as a PgError wrapped in a failed connection attempt, and "unavailable"
	// would be the wrong thing to tell the model about either.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fromPgError(cfg, pgErr, err)
	}

	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return &mcpsrv.Failure{
			Kind:   mcpsrv.KindUnavailable,
			Detail: fmt.Sprintf("cannot reach postgres at %s: %v", address(cfg.Connection), cause(connErr)),
			Err:    err,
		}
	}

	// The client-side deadline is the only limit that does not depend on the
	// server having received one, so its expiry is evidence, not just a timeout:
	// a pooler between us and postgres may be dropping statement_timeout.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &mcpsrv.Failure{
			Kind: mcpsrv.KindTimeout,
			Detail: fmt.Sprintf("the call ran past %s without the server cancelling it at timeouts.query (%s)",
				cfg.ClientDeadline(), cfg.Timeouts.Query.Duration()),
			Hint: "narrow the query; if this repeats, a connection pooler is dropping statement_timeout",
			Err:  err,
		}
	}

	if errors.Is(err, errPoolsClosed) {
		return &mcpsrv.Failure{Kind: mcpsrv.KindUnavailable, Detail: errPoolsClosed.Error(), Err: err}
	}

	return &mcpsrv.Failure{Kind: mcpsrv.KindInternal, Err: err}
}

func fromPgError(cfg Config, pgErr *pgconn.PgError, err error) *mcpsrv.Failure {
	f := &mcpsrv.Failure{Detail: pgErr.Message, Hint: pgErr.Hint, Err: err}

	switch {
	// PgBouncer in statement pooling forbids a multi-statement transaction, so
	// it is not the timeout that breaks — it is every read tool at once.
	case pgErr.Code == codeProtocolViolation && strings.Contains(pgErr.Message, "statement pooling"):
		f.Kind = mcpsrv.KindNotConfigured
		f.Detail = "the connection goes through a pooler in statement pooling mode, where the read-only transaction every read tool needs is not allowed"
		f.Hint = "point connection at postgres directly, or switch the pooler to session or transaction pooling"

	case pgErr.Code == codeQueryCanceled:
		f.Kind = mcpsrv.KindTimeout
		f.Detail = fmt.Sprintf("the server cancelled the query at timeouts.query (%s)", cfg.Timeouts.Query.Duration())
		f.Hint = "narrow the query — fewer rows, fewer joins, a smaller range"

	case pgErr.Code == codeLockNotAvailable:
		f.Kind = mcpsrv.KindTimeout
		f.Detail = fmt.Sprintf("the server gave up waiting for a lock at timeouts.lock (%s)", cfg.Timeouts.Lock.Duration())
		f.Hint = "something else is holding it; try again later"

	case pgErr.Code == codeReadOnlyTransaction:
		f.Kind = mcpsrv.KindDenied
		f.Detail = "this tool runs inside a READ ONLY transaction and the statement writes"
		f.Hint = "a write tool of this server is the only way to change anything"

	case pgErr.Code == codeInvalidCatalogName:
		f.Kind = mcpsrv.KindBadArgument
		f.Hint = "list the databases first"

	case pgErr.Code == codeInsufficientPrivs, class(pgErr.Code) == "28":
		f.Kind = mcpsrv.KindDenied

	// 08 connection, 40 rollback, 53 resources, 55 object in use, 57 operator,
	// 58 system: the statement is fine and the server is not in a state to run it.
	case slices.Contains([]string{"08", "40", "53", "55", "57", "58"}, class(pgErr.Code)):
		f.Kind = mcpsrv.KindUnavailable

	// 22 data, 23 constraint, 42 syntax or missing object, 0A unsupported:
	// the SQL is wrong, and the server's own words are what fixes it.
	case slices.Contains([]string{"22", "23", "42", "0A", "21", "2B", "2F", "44"}, class(pgErr.Code)):
		f.Kind = mcpsrv.KindBadArgument
		if pgErr.Position > 0 {
			f.Detail = fmt.Sprintf("%s (at character %d)", pgErr.Message, pgErr.Position)
		}

	default:
		return &mcpsrv.Failure{Kind: mcpsrv.KindInternal, Err: err}
	}

	return f
}

func class(code string) string {
	if len(code) < 2 {
		return ""
	}
	return code[:2]
}

func address(c Connection) string {
	if c.Port > 0 {
		return fmt.Sprintf("%s:%d", c.Host, c.Port)
	}
	return c.Host
}

// cause is the innermost error: the driver's own text names the user and the
// database, and neither belongs in an answer about a socket.
func cause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

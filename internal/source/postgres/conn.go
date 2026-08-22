package postgres

import (
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxPoolConns caps pool.maxConnsPerDatabase after validation, so the int32 the
// driver wants can never be assembled out of a negative or absurd number.
const maxPoolConns = 100

// connString renders one database of the connection as a libpq keyword/value
// string. The config keeps the parts apart (ADR-0002) and they are put back
// together only here: sslmode is not a field the driver exposes — it is TLS
// settings computed while parsing — so the string is the only way in.
func connString(c Connection, database string) string {
	kv := [][2]string{
		{"host", c.Host},
		{"user", c.User},
		{"password", c.Password},
		{"dbname", database},
		{"sslmode", string(c.SSLMode)},
	}
	if c.Port > 0 {
		kv = append(kv, [2]string{"port", strconv.Itoa(c.Port)})
	}

	var b strings.Builder
	for _, p := range kv {
		if p[1] == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p[0])
		b.WriteString("='")
		b.WriteString(quoteValue(p[1]))
		b.WriteString("'")
	}
	return b.String()
}

// quoteValue escapes a value for the single-quoted form of a keyword/value
// string: a password with a space or a quote in it is a password, not a syntax
// error.
func quoteValue(v string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v)
}

// poolConfig is the driver config for one database.
func poolConfig(cfg Config, database string) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig(connString(cfg.Connection, database))
	if err != nil {
		return nil, err
	}

	conns := min(max(cfg.Pool.MaxConnsPerDatabase, 1), maxPoolConns)
	pc.MaxConns = int32(conns) //nolint:gosec // G115: clamped to [1, maxPoolConns] just above
	// Nothing is opened until a call needs it, and an unused connection goes
	// away on its own: both halves of "lazy" (ADR-0003).
	pc.MinConns = 0
	pc.MinIdleConns = 0
	pc.MaxConnIdleTime = cfg.Pool.IdleTimeout.Duration()
	pc.ConnConfig.ConnectTimeout = cfg.Timeouts.Connect.Duration()
	// Unnamed prepared statements: a named one is cached per connection, and a
	// PgBouncer in transaction pooling hands the next call a server connection
	// that never saw it.
	pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	return pc, nil
}

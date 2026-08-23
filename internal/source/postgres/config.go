// Package postgres is the reference source: the postgres server the other five
// are cloned from.
package postgres

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"time"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

// Name is the source name: the config file prefix, the environment variable
// and the binary suffix all derive from it.
const Name = "postgres"

// entryDatabase is the default databases.default: initdb creates it on every
// cluster, so a config that names no entry point still has one to read the
// catalog through.
const entryDatabase = "postgres"

// Config is the whole postgres server config, and — once the core has applied
// inheritance — the config in effect at one address. The groups the core reads
// come from the embedded skeleton; the embedded [Cluster] is the global level,
// what a cluster inherits unless it says otherwise.
type Config struct {
	mcpsrv.Common[Cluster, ReadTools]
	Cluster
}

// Cluster is one postgres server: everything that may differ between clusters,
// and so everything that inherits down the three levels of the file. Every key
// is optional at every level — what a cluster ends up needing is [Config.Validate]'s
// business, once inheritance has had its say.
type Cluster struct {
	mcpsrv.ClusterCommon

	Connection Connection `json:"connection,omitzero" jsonschema:"how to reach the postgres server"`
	Databases  Databases  `json:"databases,omitzero" jsonschema:"which databases the tools may touch"`
	Pool       Pool       `json:"pool,omitzero" jsonschema:"connection pooling"`
	Timeouts   Timeouts   `json:"timeouts,omitzero" jsonschema:"query and connection deadlines"`
}

// Connection addresses the server, not a database: which database a tool talks
// to is an argument of that tool. Structured fields rather than a DSN, so that
// "the password must be a ${VAR}" is a check on one field instead of a parse of
// a URL.
type Connection struct {
	Host     string  `json:"host,omitzero" jsonschema:"postgres host"`
	Port     int     `json:"port,omitzero" jsonschema:"postgres port"`
	User     string  `json:"user,omitzero" jsonschema:"role to connect as"`
	Password string  `json:"password,omitzero" jsonschema:"password as ${VAR}, naming an environment variable" mcpsrv:"secret"`
	SSLMode  SSLMode `json:"sslmode,omitzero" jsonschema:"libpq sslmode"`
}

// SSLMode is the libpq sslmode. Its values live in the schema, not in a Go
// check: an editor rejects a typo while it is being typed.
type SSLMode string

// The libpq sslmode values, weakest to strongest.
const (
	SSLDisable    SSLMode = "disable"
	SSLPrefer     SSLMode = "prefer"
	SSLRequire    SSLMode = "require"
	SSLVerifyCA   SSLMode = "verify-ca"
	SSLVerifyFull SSLMode = "verify-full"
)

// Databases bounds what the tools can reach: an empty include is the whole
// cluster, a non-empty one only what it names, and exclude is subtracted after.
// default is neither — it is where a call that names no database connects, and
// it passes the lists like any other name to be reachable by one.
type Databases struct {
	Default string   `json:"default,omitzero" jsonschema:"database the catalog queries connect to; reachable by name only if include and exclude let it through"`
	Include []string `json:"include,omitzero" jsonschema:"glob patterns of the databases tools may reach; empty means every database of the cluster"`
	Exclude []string `json:"exclude,omitzero" jsonschema:"glob patterns of databases to hide, subtracted after include"`
}

// Pool caps how much of someone else's server we occupy: maxDatabases times
// maxConnsPerDatabase is the ceiling.
type Pool struct {
	MaxDatabases        int             `json:"maxDatabases,omitzero" jsonschema:"how many databases stay open at once"`
	MaxConnsPerDatabase int             `json:"maxConnsPerDatabase,omitzero" jsonschema:"pool size for one database"`
	IdleTimeout         mcpsrv.Duration `json:"idleTimeout,omitzero" jsonschema:"how long an unused pool stays open"`
	EagerInit           bool            `json:"eagerInit,omitzero" jsonschema:"connect at startup instead of on the first tool call"`
}

// Timeouts are the server-side limits. The client-side deadline is not a key
// here — see [Config.ClientDeadline].
type Timeouts struct {
	Query   mcpsrv.Duration `json:"query,omitzero" jsonschema:"SET LOCAL statement_timeout for a read transaction"`
	Lock    mcpsrv.Duration `json:"lock,omitzero" jsonschema:"SET LOCAL lock_timeout for a read transaction"`
	Connect mcpsrv.Duration `json:"connect,omitzero" jsonschema:"limit on establishing a connection"`
}

// ReadTools is the read half of the tools group.
type ReadTools struct {
	// ExtraDenyFunctions only ever adds: the built-in list is not printed into
	// the config file, so nobody is invited to edit it down.
	ExtraDenyFunctions []string `json:"extraDenyFunctions,omitzero" jsonschema:"globs of function names to add to the built-in deny list"`
}

// Defaults is the config each level of a file is applied on top of. Cluster
// keys are in it too: a cluster that names none of them still needs a pool and
// a set of timeouts.
func Defaults() Config {
	c := Config{
		Cluster: Cluster{
			Connection: Connection{Port: 5432, SSLMode: SSLPrefer},
			Databases:  Databases{Default: entryDatabase},
			Pool: Pool{
				MaxDatabases:        4,
				MaxConnsPerDatabase: 2,
				IdleTimeout:         mcpsrv.Duration(5 * time.Minute),
			},
			Timeouts: Timeouts{
				Query:   mcpsrv.Duration(30 * time.Second),
				Lock:    mcpsrv.Duration(5 * time.Second),
				Connect: mcpsrv.Duration(5 * time.Second),
			},
		},
	}
	c.Output = mcpsrv.Output{MaxRows: 200, MaxBytes: 32768, MaxCellChars: 200}
	c.Tools.Write.RequireConfirmation = true
	return c
}

// Minimal is what --init writes: one environment holding one cluster, with the
// keys nobody can guess filled in as placeholders, and nothing else.
// databases.default is one of them although it has a default: postgres is a
// database that exists, not the entry point a given deployment wants.
func Minimal() Config {
	var c Config
	c.Environments = map[string]mcpsrv.Environment[Cluster]{
		"dev": {Clusters: map[string]Cluster{
			"main": {
				Connection: Connection{Host: "db.example.com", User: "app", Password: "${PGPASSWORD}"},
				Databases:  Databases{Default: "app_db"},
			},
		}},
	}
	return c
}

// ConfigTypes carries the constraints the jsonschema tag cannot express.
func ConfigTypes() mcpsrv.TypeSchemas {
	return mcpsrv.TypeSchemas{
		reflect.TypeFor[SSLMode](): {
			Type: "string",
			Enum: []any{
				string(SSLDisable), string(SSLPrefer), string(SSLRequire),
				string(SSLVerifyCA), string(SSLVerifyFull),
			},
		},
	}
}

// Validate covers what the schema cannot state, and runs on one cluster's
// config. Presence is part of that now: a key may arrive from any of the three
// levels, so the schema can require it at none of them.
func (c *Config) Validate() error {
	for _, f := range []struct{ key, value string }{
		{"connection.host", c.Connection.Host},
		{"connection.user", c.Connection.User},
		{"connection.password", c.Connection.Password},
	} {
		if f.value == "" {
			return fmt.Errorf("%s: no value at this address, and none inherited", f.key)
		}
	}
	// Not a presence check — the key has a default. An explicit "" overrides it,
	// and libpq would then read the role name as the database name.
	if c.Databases.Default == "" {
		return fmt.Errorf("databases.default: an empty string is not a database; leave the key out for %q", entryDatabase)
	}
	// An explicit zero in the file overrides the default, and a pool sized zero
	// would be quietly rounded up to something that works — a key that does
	// nothing is worse than a config that is refused.
	if c.Pool.MaxDatabases < 1 {
		return fmt.Errorf("pool.maxDatabases: %d databases is not a pool", c.Pool.MaxDatabases)
	}
	if n := c.Pool.MaxConnsPerDatabase; n < 1 || n > maxPoolConns {
		return fmt.Errorf("pool.maxConnsPerDatabase: %d is outside 1..%d", n, maxPoolConns)
	}
	// Zero would disable the server-side limit but not [Config.ClientDeadline],
	// which is derived from it — the call would still be cut off, and the model
	// would be told the server failed to cancel a limit that was never set.
	if c.Timeouts.Query <= 0 {
		return errors.New("timeouts.query: a query needs a limit; the client deadline is derived from it")
	}
	// path.Match scans the whole pattern before it gives up on a name, so the
	// empty one is enough to reject a glob that would only ever error at a call.
	for _, list := range []struct {
		key      string
		patterns []string
	}{
		{"databases.include", c.Databases.Include},
		{"databases.exclude", c.Databases.Exclude},
		// An invalid glob here would be a hole, not a mismatch: path.Match's
		// error is dropped at the call, and the function would go through.
		{"tools.read.extraDenyFunctions", c.Tools.Read.ExtraDenyFunctions},
	} {
		for _, pat := range list.patterns {
			if _, err := path.Match(pat, ""); err != nil {
				return fmt.Errorf("%s: %q is not a valid glob: %w", list.key, pat, err)
			}
		}
	}
	return nil
}

// ClientDeadline is the deadline the Go side enforces, fixed at query + 5s. It
// is a backstop for a statement_timeout that never reached the server, not a
// knob: set below the server limit it would produce two explanations for one
// cancellation.
func (c *Config) ClientDeadline() time.Duration {
	return c.Timeouts.Query.Duration() + 5*time.Second
}

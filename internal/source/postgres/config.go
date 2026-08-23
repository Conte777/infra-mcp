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

// Databases bounds what the tools can reach. With showAll off, default is the
// only database they see.
type Databases struct {
	Default string   `json:"default,omitzero" jsonschema:"database to connect to, and the only one reachable unless showAll is set"`
	ShowAll bool     `json:"showAll,omitzero" jsonschema:"allow tools into every database of the cluster"`
	Exclude []string `json:"exclude,omitzero" jsonschema:"glob patterns of databases to hide when showAll is set"`
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
	ExtraDenyFunctions []string `json:"extraDenyFunctions,omitzero" jsonschema:"function names to add to the built-in deny list"`
}

// Defaults is the config each level of a file is applied on top of. Cluster
// keys are in it too: a cluster that names none of them still needs a pool and
// a set of timeouts.
func Defaults() Config {
	c := Config{
		Cluster: Cluster{
			Connection: Connection{Port: 5432, SSLMode: SSLPrefer},
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
// keys that have no default filled in as placeholders, and nothing else.
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
		{"databases.default", c.Databases.Default},
	} {
		if f.value == "" {
			return fmt.Errorf("%s: no value at this address, and none inherited", f.key)
		}
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
	for _, pat := range c.Databases.Exclude {
		ok, err := path.Match(pat, c.Databases.Default)
		if err != nil {
			return fmt.Errorf("databases.exclude: %q is not a valid glob: %w", pat, err)
		}
		// Showing a database told to hide is a surprise; hiding the one we
		// connect to is a broken server. Neither may happen silently.
		if ok {
			return fmt.Errorf("databases.exclude: %q hides databases.default %q", pat, c.Databases.Default)
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

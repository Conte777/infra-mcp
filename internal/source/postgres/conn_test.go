package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

func TestConnString(t *testing.T) {
	tests := []struct {
		name string
		conn Connection
		db   string
		want string
	}{
		{
			name: "every part present",
			conn: Connection{Host: "db.example.com", Port: 6432, User: "app", Password: "s3cret", SSLMode: SSLRequire},
			db:   "app_db",
			want: "host='db.example.com' user='app' password='s3cret' dbname='app_db' sslmode='require' port='6432'",
		},
		{
			name: "no password and no port",
			conn: Connection{Host: "/var/run/postgresql", User: "app"},
			db:   "app_db",
			want: "host='/var/run/postgresql' user='app' dbname='app_db'",
		},
		{
			name: "a quote in the password is a password",
			conn: Connection{Host: "h", User: "u", Password: `it's a \ pass`},
			db:   "d",
			want: `host='h' user='u' password='it\'s a \\ pass' dbname='d'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connString(tt.conn, tt.db); got != tt.want {
				t.Errorf("connString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A password with a quote in it must survive the round trip, not just look
// escaped: the driver's parser is the only judge of that.
func TestConnStringParsesBack(t *testing.T) {
	c := Connection{Host: "db.example.com", Port: 6432, User: "app", Password: `it's a \ pass`, SSLMode: SSLDisable}

	parsed, err := pgx.ParseConfig(connString(c, "app_db"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if parsed.Password != c.Password {
		t.Errorf("password = %q, want %q", parsed.Password, c.Password)
	}
	if parsed.Host != c.Host || parsed.Port != uint16(c.Port) || parsed.Database != "app_db" {
		t.Errorf("got %s:%d/%s", parsed.Host, parsed.Port, parsed.Database)
	}
}

func TestPoolConfig(t *testing.T) {
	cfg := Defaults()
	cfg.Connection = Connection{Host: "db.example.com", User: "app", SSLMode: SSLDisable}
	cfg.Pool.MaxConnsPerDatabase = 1 << 20
	cfg.Timeouts.Connect = mcpsrv.Duration(1500 * time.Millisecond)

	pc, err := poolConfig(cfg, "app_db")
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	if pc.MaxConns != maxPoolConns {
		t.Errorf("MaxConns = %d, want the clamp %d", pc.MaxConns, maxPoolConns)
	}
	if pc.MinConns != 0 || pc.MinIdleConns != 0 {
		t.Errorf("MinConns = %d, MinIdleConns = %d, want a pool that opens nothing on its own", pc.MinConns, pc.MinIdleConns)
	}
	if pc.MaxConnIdleTime != cfg.Pool.IdleTimeout.Duration() {
		t.Errorf("MaxConnIdleTime = %s, want %s", pc.MaxConnIdleTime, cfg.Pool.IdleTimeout.Duration())
	}
	if pc.ConnConfig.ConnectTimeout != 1500*time.Millisecond {
		t.Errorf("ConnectTimeout = %s, want 1.5s", pc.ConnConfig.ConnectTimeout)
	}
	if pc.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Errorf("DefaultQueryExecMode = %v, want QueryExecModeExec", pc.ConnConfig.DefaultQueryExecMode)
	}
}

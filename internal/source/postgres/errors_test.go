package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

func TestFailureFromPgError(t *testing.T) {
	tests := []struct {
		name       string
		pg         *pgconn.PgError
		want       mcpsrv.Kind
		wantDetail string // substring
	}{
		{"statement timeout", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, mcpsrv.KindTimeout, "timeouts.query"},
		{"lock timeout", &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}, mcpsrv.KindTimeout, "timeouts.lock"},
		{"write in a read-only transaction", &pgconn.PgError{Code: "25006", Message: "cannot execute DELETE in a read-only transaction"}, mcpsrv.KindDenied, "READ ONLY"},
		{"no such database", &pgconn.PgError{Code: "3D000", Message: `database "nope" does not exist`}, mcpsrv.KindBadArgument, `"nope"`},
		{"no privilege", &pgconn.PgError{Code: "42501", Message: "permission denied for table t"}, mcpsrv.KindDenied, "permission denied"},
		{"bad password", &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}, mcpsrv.KindDenied, "authentication"},
		{"syntax error", &pgconn.PgError{Code: "42601", Message: `syntax error at or near "slect"`, Position: 1}, mcpsrv.KindBadArgument, "at character 1"},
		{"no such table", &pgconn.PgError{Code: "42P01", Message: `relation "nope" does not exist`}, mcpsrv.KindBadArgument, "does not exist"},
		{"division by zero", &pgconn.PgError{Code: "22012", Message: "division by zero"}, mcpsrv.KindBadArgument, "division"},
		{"too many connections", &pgconn.PgError{Code: "53300", Message: "too many clients already"}, mcpsrv.KindUnavailable, "too many"},
		{"deadlock", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}, mcpsrv.KindUnavailable, "deadlock"},
		{"shutting down", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, mcpsrv.KindUnavailable, "terminating"},
		{"unclassified keeps no text", &pgconn.PgError{Code: "XX000", Message: "internal error: cache lookup failed"}, mcpsrv.KindInternal, ""},
		{
			"a pooler in statement pooling mode",
			&pgconn.PgError{Code: "08P01", Message: "transaction blocks not allowed in statement pooling mode"},
			mcpsrv.KindNotConfigured, "statement pooling mode",
		},
	}

	cfg := Defaults()
	cfg.Databases.Default = "app_db"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f *mcpsrv.Failure
			if !errors.As(failure(context.Background(), cfg, tt.pg), &f) {
				t.Fatal("want a *mcpsrv.Failure")
			}
			if f.Kind != tt.want {
				t.Errorf("kind = %v, want %v", f.Kind, tt.want)
			}
			if !strings.Contains(f.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", f.Detail, tt.wantDetail)
			}
			if !errors.Is(f, tt.pg) {
				t.Error("the cause must stay reachable for the log")
			}
		})
	}
}

// The driver's own text names the user and the database it tried; an answer
// about an unreachable socket says host and port and nothing else.
func TestFailureFromConnectError(t *testing.T) {
	cfg := Defaults()
	cfg.Connection = Connection{Host: "db.example.com", Port: 6432, User: "app", Password: "s3cret"}

	connErr := &pgconn.ConnectError{
		Config: &pgconn.Config{User: "app", Database: "app_db"},
		// The driver builds the wrapped error itself; a plain one stands in.
	}
	err := failure(context.Background(), cfg, errors.Join(connErr))

	var f *mcpsrv.Failure
	if !errors.As(err, &f) {
		t.Fatal("want a *mcpsrv.Failure")
	}
	if f.Kind != mcpsrv.KindUnavailable {
		t.Errorf("kind = %v, want unavailable", f.Kind)
	}
	if !strings.Contains(f.Detail, "db.example.com:6432") {
		t.Errorf("detail = %q, want the address in it", f.Detail)
	}
	for _, leak := range []string{"s3cret", "user=app"} {
		if strings.Contains(f.Detail, leak) {
			t.Errorf("detail = %q, must not carry %q", f.Detail, leak)
		}
	}
}

// The client-side deadline is the only limit that does not depend on the server
// having received one, so its expiry names the pooler that may have eaten it.
func TestFailureFromClientDeadline(t *testing.T) {
	cfg := Defaults()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var f *mcpsrv.Failure
	if !errors.As(failure(ctx, cfg, context.DeadlineExceeded), &f) {
		t.Fatal("want a *mcpsrv.Failure")
	}
	if f.Kind != mcpsrv.KindTimeout {
		t.Errorf("kind = %v, want timeout", f.Kind)
	}
	if !strings.Contains(f.Hint, "pooler") {
		t.Errorf("hint = %q, want the dropped statement_timeout named", f.Hint)
	}
}

func TestFailurePassesAFailureThrough(t *testing.T) {
	cfg := Defaults()
	want := &mcpsrv.Failure{Kind: mcpsrv.KindDenied, Detail: "already classified"}

	if got := failure(context.Background(), cfg, want); !errors.Is(got, want) {
		t.Errorf("failure() = %v, want the same failure back", got)
	}
	if got := failure(context.Background(), cfg, nil); got != nil {
		t.Errorf("failure(nil) = %v, want nil", got)
	}
}

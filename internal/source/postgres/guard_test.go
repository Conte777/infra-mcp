package postgres

import (
	"errors"
	"testing"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

func TestFirstKeyword(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"plain", "SELECT 1", "select"},
		{"leading line comment", "-- find the users\nSELECT 1", "select"},
		{"line comment without a newline", "-- nothing after this", ""},
		{"block comment", "/* plan */ WITH x AS (SELECT 1) SELECT * FROM x", "with"},
		{"nested block comment", "/* a /* b */ c */ TABLE t", "table"},
		{"unterminated block comment", "/* forever", ""},
		{"parenthesised query", "((SELECT 1) UNION (SELECT 2))", "select"},
		{"empty", "   \n\t ", ""},
		{"write", "delete from t", "delete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstKeyword(tt.sql); got != tt.want {
				t.Errorf("firstKeyword(%q) = %q, want %q", tt.sql, got, tt.want)
			}
		})
	}
}

func TestGuardRead(t *testing.T) {
	cfg := Defaults()
	cfg.Tools.Read.ExtraDenyFunctions = []string{"pg_promote"}

	tests := []struct {
		name string
		sql  string
		want mcpsrv.Kind
		ok   bool
	}{
		{name: "select", sql: "SELECT * FROM t", ok: true},
		{name: "show", sql: "SHOW work_mem", ok: true},
		{name: "explain", sql: "EXPLAIN SELECT 1", ok: true},
		{name: "empty", sql: "  ", want: mcpsrv.KindBadArgument},
		{name: "update", sql: "UPDATE t SET a = 1", want: mcpsrv.KindDenied},
		{name: "delete hidden behind a comment", sql: "-- read\nDELETE FROM t", want: mcpsrv.KindDenied},
		{name: "denied function", sql: "SELECT pg_read_file('/etc/passwd')", want: mcpsrv.KindDenied},
		{name: "denied through the schema", sql: "SELECT pg_catalog.pg_terminate_backend(1)", want: mcpsrv.KindDenied},
		{name: "denied with a space before the call", sql: "SELECT dblink ('a', 'b')", want: mcpsrv.KindDenied},
		{name: "denied from the config", sql: "SELECT pg_promote()", want: mcpsrv.KindDenied},
		{name: "the name as data is not a call", sql: "SELECT * FROM t WHERE fn = 'pg_read_file'", ok: true},
		{name: "a longer identifier is not the denied one", sql: "SELECT my_pg_read_file_wrapper()", ok: true},
		{name: "copy to program", sql: "COPY t TO PROGRAM 'sh -c evil'", want: mcpsrv.KindDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guardRead(cfg, tt.sql)
			if tt.ok {
				if err != nil {
					t.Fatalf("guardRead(%q) = %v, want it allowed", tt.sql, err)
				}
				return
			}
			var f *mcpsrv.Failure
			if !errors.As(err, &f) {
				t.Fatalf("guardRead(%q) = %v, want a *mcpsrv.Failure", tt.sql, err)
			}
			if f.Kind != tt.want {
				t.Errorf("kind = %v (%v), want %v", f.Kind, f, tt.want)
			}
		})
	}
}

// The guard is the layer that must not be silent: a refusal names what it
// refused, or the model retries the same statement.
func TestGuardRefusalNamesTheReason(t *testing.T) {
	var f *mcpsrv.Failure
	if !errors.As(guardRead(Defaults(), "SELECT dblink('a', 'b')"), &f) {
		t.Fatal("a denied function must fail")
	}
	if f.Detail == "" || f.Hint == "" {
		t.Errorf("detail = %q, hint = %q; want the function named and a way out", f.Detail, f.Hint)
	}
}

func TestTrimStatement(t *testing.T) {
	tests := []struct{ in, want string }{
		{"SELECT 1;", "SELECT 1"},
		{" SELECT 1 ;  \n", "SELECT 1"},
		{"SELECT 1", "SELECT 1"},
		{"SELECT ';'", "SELECT ';'"},
	}
	for _, tt := range tests {
		if got := trimStatement(tt.in); got != tt.want {
			t.Errorf("trimStatement(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

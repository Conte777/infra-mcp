package postgres

import (
	"errors"
	"strings"
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
		{"nothing after the keyword", "select", "select"},
		{"a quoted identifier is not a keyword", `"select" 1`, ""},
		{"a literal does not start a statement", "'select' 1", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstKeyword(tokens(tt.sql)); got != tt.want {
				t.Errorf("firstKeyword(%q) = %q, want %q", tt.sql, got, tt.want)
			}
		})
	}
}

func TestGuardRead(t *testing.T) {
	cfg := Defaults()
	cfg.Tools.Read.ExtraDenyFunctions = []string{"pgstattuple"}

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
		{name: "denied from the config", sql: "SELECT pgstattuple('t')", want: mcpsrv.KindDenied},
		{name: "a comment between the name and the call", sql: "SELECT pg_read_file/**/('/etc/passwd')", want: mcpsrv.KindDenied},
		{name: "a quoted identifier is the same call", sql: `SELECT "pg_read_file"('/etc/passwd')`, want: mcpsrv.KindDenied},
		{name: "a comment inside the name is a different name", sql: "SELECT pg_read/**/_file('/etc/passwd')", ok: true},
		{name: "a session-level advisory lock outlives the rollback", sql: "SELECT pg_advisory_lock(42)", want: mcpsrv.KindDenied},
		{name: "set_config would drop the statement timeout", sql: "SELECT set_config('statement_timeout', '0', true)", want: mcpsrv.KindDenied},
		{name: "the name as data is not a call", sql: "SELECT * FROM t WHERE fn = 'pg_read_file'", ok: true},
		{name: "a longer identifier is not the denied one", sql: "SELECT my_pg_read_file_wrapper()", ok: true},
		{name: "an advisory lock the rollback releases", sql: "SELECT pg_advisory_xact_lock(42)", ok: true},
		{name: "a large object read is not a file read", sql: "SELECT lo_get(1234)", ok: true},
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

// Every function the deny list names, one case per family: the list is what the
// third layer is, and a family that quietly stops matching is invisible until
// someone calls it (#66).
func TestDenyListCoversTheClass(t *testing.T) {
	denied := []struct {
		group string
		calls []string
	}{
		{"server files", []string{
			"pg_read_file('/etc/passwd')", "pg_read_binary_file('/etc/passwd')",
			"pg_stat_file('/etc/passwd')", "pg_ls_dir('/')",
		}},
		{"the pg_ls family beyond pg_ls_dir", []string{
			"pg_ls_logdir()", "pg_ls_waldir()", "pg_ls_tmpdir()", "pg_ls_archive_statusdir()",
			"pg_ls_summariesdir()", "pg_ls_logicalmapdir()", "pg_ls_logicalsnapdir()",
			"pg_ls_replslotdir()",
		}},
		{"adminpack writes a file", []string{
			"pg_file_write('/root/.ssh/authorized_keys', 'key', false)",
			"pg_file_rename('a', 'b')", "pg_file_unlink('a')", "pg_file_sync('a')",
			"pg_logdir_ls()",
		}},
		{"large object import and export", []string{"lo_import('/etc/passwd')", "lo_export(1, '/tmp/x')"}},
		{"dblink opens a connection", []string{
			"dblink('h', 'q')", "dblink_connect('host=10.0.0.1 port=22')",
			"dblink_connect_u('host=10.0.0.1')", "dblink_exec('c', 'q')",
			"dblink_open('c', 'q')", "dblink_send_query('c', 'q')",
		}},
		{"backends and the server process", []string{
			"pg_terminate_backend(1)", "pg_cancel_backend(1)",
			"pg_reload_conf()", "pg_rotate_logfile()", "pg_promote()",
		}},
		{"session advisory locks outlive the rollback", []string{
			"pg_advisory_lock(1)", "pg_advisory_lock_shared(1)",
			"pg_try_advisory_lock(1)", "pg_try_advisory_lock_shared(1)",
		}},
		{"statistics do not come back", []string{
			"pg_stat_reset()", "pg_stat_reset_shared('io')", "pg_stat_reset_slru('a')",
			"pg_stat_reset_backend_stats(1)", "pg_stat_reset_replication_slot('s')",
			"pg_stat_reset_subscription_stats(1)", "pg_stat_reset_single_table_counters(1)",
			"pg_stat_reset_single_function_counters(1)",
		}},
		{"replication slots and origins", []string{
			"pg_logical_slot_get_changes('s', NULL, NULL)",
			"pg_logical_slot_get_binary_changes('s', NULL, NULL)",
			"pg_replication_origin_create('o')", "pg_replication_origin_drop('o')",
			"pg_replication_origin_advance('o', '0/0')",
			"pg_replication_origin_session_setup('o')", "pg_replication_origin_session_reset()",
			"pg_drop_replication_slot('s')", "pg_replication_slot_advance('s', '0/0')",
			"pg_create_logical_replication_slot('s', 'pgoutput')",
			"pg_create_physical_replication_slot('s')",
			"pg_copy_logical_replication_slot('a', 'b')",
			"pg_copy_physical_replication_slot('a', 'b')",
			"pg_sync_replication_slots()",
		}},
		{"wal and backup", []string{
			"pg_switch_wal()", "pg_create_restore_point('r')",
			"pg_backup_start('l')", "pg_backup_stop()",
			"pg_start_backup('l')", "pg_stop_backup()",
			"pg_wal_replay_pause()", "pg_wal_replay_resume()",
		}},
		{"a wal message needs no superuser", []string{
			"pg_logical_emit_message(false, 'p', 'm')", "pg_log_standby_snapshot()",
			"pg_log_backend_memory_contexts(1)",
		}},
		{"the rest", []string{"pg_import_system_collations('pg_catalog')", "set_config('statement_timeout', '0', true)"}},
	}

	for _, group := range denied {
		t.Run(group.group, func(t *testing.T) {
			for _, call := range group.calls {
				sql := "SELECT " + call
				var f *mcpsrv.Failure
				if !errors.As(guardRead(Defaults(), sql), &f) || f.Kind != mcpsrv.KindDenied {
					t.Errorf("guardRead(%q) let it through", sql)
					continue
				}
				// The pattern that denied it is a glob as often as not, so the
				// refusal has to name the call the model actually wrote.
				name := call[:strings.IndexByte(call, '(')]
				if !strings.Contains(f.Detail, name) {
					t.Errorf("detail = %q, want it to name %s", f.Detail, name)
				}
				// An exact entry would otherwise print the name twice.
				if strings.Contains(f.Detail, "("+name+")") {
					t.Errorf("detail = %q, want the pattern left out when it is the name", f.Detail)
				}
			}
		})
	}
}

// A glob is only worth its reach if it cannot swallow a name the project decided
// to allow.
func TestDenyGlobsDoNotOverreach(t *testing.T) {
	allowed := []string{
		"SELECT pg_advisory_xact_lock(1)",
		"SELECT pg_advisory_xact_lock_shared(1)",
		"SELECT pg_advisory_unlock_all()",
		"SELECT lo_get(1)",
		"SELECT lo_open(1, 1)",
		"SELECT pg_stat_get_replication_slot('s')",
		"SELECT pg_current_logfile()",
		"SELECT pg_relation_filepath('t')",
		// Peeking reads the same changes without advancing the slot, and these
		// four origin functions only report where replication got to — the
		// diagnostic queries the deny list exists to leave alone.
		"SELECT pg_logical_slot_peek_changes('s', NULL, NULL)",
		"SELECT pg_logical_slot_peek_binary_changes('s', NULL, NULL)",
		"SELECT pg_replication_origin_oid('o')",
		"SELECT pg_replication_origin_progress('o', true)",
		"SELECT pg_replication_origin_session_progress(true)",
		"SELECT pg_replication_origin_session_is_setup()",
		"SELECT pg_replication_origin_xact_setup('0/0', now())",
		"SELECT pg_replication_origin_xact_reset()",
	}
	for _, sql := range allowed {
		if err := guardRead(Defaults(), sql); err != nil {
			t.Errorf("guardRead(%q) = %v, want it allowed", sql, err)
		}
	}
}

func TestCalledNames(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"plain call", "select f(1)", []string{"f"}},
		{"schema qualified", "select pg_catalog.pg_ls_dir('/')", []string{"pg_ls_dir"}},
		{"space before the arguments", "select f ()", []string{"f"}},
		{"not applied", "select a from t where b = 1", nil},
		{"name at the end", "select f", nil},
		{"nested", "select f(g(1))", []string{"f", "g"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calledNames(tokens(tt.sql))
			if len(got) != len(tt.want) {
				t.Fatalf("calledNames(%q) = %v, want %v", tt.sql, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("calledNames(%q) = %v, want %v", tt.sql, got, tt.want)
				}
			}
		})
	}
}

// The second lock over COPY (#65). Nothing reaches it through guardRead any
// more — COPY cannot start a readable statement, and the lock no longer reads
// inside a literal — so it is pinned at the level it runs at (#71).
func TestCopiesToProgram(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{"to program", "COPY t TO PROGRAM 'sh -c evil'", true},
		{"from program", "COPY t FROM PROGRAM 'cat /etc/shadow'", true},
		{"the literal abuts the keyword", "COPY t TO PROGRAM'sh -c evil'", true},
		{"already lower case", "copy t to program 'x'", true},
		{"newlines and tabs between the words", "COPY t\n\tTO\n\tPROGRAM\t'x'", true},
		{"a comment between to and program", "COPY t TO/**/PROGRAM 'x'", true},
		// A dollar sign continues an identifier, so postgres reads program$$x$$
		// as one name and refuses the statement itself.
		{"dollar quoting abuts the keyword", "COPY t TO PROGRAM$$x$$", false},
		{"a table called program", "SELECT * FROM program.copy", false},
		{"a quoted program is a name, not the keyword", `COPY t TO "PROGRAM" 'x'`, false},
		{"copy without a program", "COPY t TO STDOUT", false},
		{"copy from a file is not a program", "COPY t FROM '/tmp/f'", false},
		{"program without the preposition", "COPY program TO STDOUT", false},
		{"program is the first word", "PROGRAM copy to stdout", false},
		{"program without a copy", "SELECT * FROM t WHERE note = 'send to program'", false},
		{"the preposition and the literal without a copy", "SELECT x FROM program 'y'", false},
		{"program ends the statement", "COPY t TO PROGRAM", false},
		{"the words in the wrong order", "SELECT 'program to copy'", false},
		// A literal holds no statement, so unlike the deny list this lock is not
		// read into one — the false refusal it used to cost is gone (#71).
		{"a literal that reads like a copy", "SELECT 'copy the log to program dir'", false},
		{"the whole command inside a literal", `SELECT 'copy t to program ''sh -c evil'''`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := copiesToProgram(tokens(tt.sql)); got != tt.want {
				t.Errorf("copiesToProgram(tokens(%q)) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}

// COPY cannot start a readable statement, so the keyword check takes every real
// one first — but EXPLAIN can carry it past that check, and the second lock is
// what refuses it. Postgres would refuse the statement too; the lock is the
// layer that must not need it to (ADR-0001).
func TestSecondLockOverCopyIsWiredIn(t *testing.T) {
	var f *mcpsrv.Failure
	if !errors.As(guardRead(Defaults(), "EXPLAIN COPY t TO PROGRAM 'sh -c evil'"), &f) {
		t.Fatal("a statement carrying COPY … TO/FROM PROGRAM must fail")
	}
	if f.Kind != mcpsrv.KindDenied {
		t.Errorf("kind = %v, want %v", f.Kind, mcpsrv.KindDenied)
	}
	if !strings.Contains(f.Detail, "PROGRAM") {
		t.Errorf("detail = %q, want it to name what it refused", f.Detail)
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

// Every way the deny list came off before it read the statement as postgres
// lexes it: an escaped identifier spells any name it likes, and a comment marker
// inside a literal blinds everything after it (#71). The refusal has to name the
// call, not the disguise.
func TestDenyListSurvivesLexicalDisguise(t *testing.T) {
	denied := []struct{ name, sql, call string }{
		{"unicode escape", `SELECT U&"pg_read_fil\0065"('/etc/passwd')`, "pg_read_file"},
		{"unicode escape, lower case prefix", `SELECT u&"set_confi\0067"('statement_timeout', '0', false)`, "set_config"},
		{"the six digit escape", `SELECT U&"pg_ls_di\+000072"('/')`, "pg_ls_dir"},
		{"uescape picks another character", `SELECT U&"pg_ls_di!0072" UESCAPE '!' ('/')`, "pg_ls_dir"},
		{"a line comment marker inside a literal", "SELECT '--' AS m, pg_ls_dir('/')", "pg_ls_dir"},
		{"a block comment marker inside a literal", "SELECT '/*' AS m, pg_ls_dir('/')", "pg_ls_dir"},
		{"a comment marker inside a dollar quote", "SELECT $$--$$ AS m, pg_ls_dir('/')", "pg_ls_dir"},
		{"an escaped apostrophe does not open a literal", `SELECT E'\'' AS m, pg_ls_dir('/')`, "pg_ls_dir"},
		{"a doubled apostrophe does not open one either", "SELECT 'it''s' AS m, pg_ls_dir('/')", "pg_ls_dir"},
		// The deny list reads inside a literal on purpose: a name assembled there
		// is passed to something that calls it (ADR-0001).
		{"a call assembled inside a literal", "SELECT 'pg_read_file(' || '/etc/passwd)'", "pg_read_file"},
		{"a call further into a literal", "SELECT 'x, pg_read_file(' || '/etc/passwd)'", "pg_read_file"},
		{"a call quoted inside a dollar quote", "SELECT $q$pg_read_file('/etc/passwd')$q$", "pg_read_file"},
		{"a hex escape inside an E-string", `SELECT E'set_confi\x67(''a'',''b'',false)'`, "set_config"},
		{"an octal escape inside an E-string", `SELECT E'lo_expor\164(1,''/tmp/x'')'`, "lo_export"},
		{"a unicode escape inside an E-string", `SELECT E'dblin\u006b(''h'',''s'')'`, "dblink"},
		{"a name split across joined constants", "SELECT 'pg_ls_di'\n'r(''.'')'", "pg_ls_dir"},
	}
	for _, tt := range denied {
		t.Run(tt.name, func(t *testing.T) {
			var f *mcpsrv.Failure
			if !errors.As(guardRead(Defaults(), tt.sql), &f) || f.Kind != mcpsrv.KindDenied {
				t.Fatalf("guardRead(%q) let it through", tt.sql)
			}
			if !strings.Contains(f.Detail, tt.call) {
				t.Errorf("detail = %q, want it to name %s", f.Detail, tt.call)
			}
		})
	}

	allowed := []struct{ name, sql string }{
		{"a non-ascii name written with escapes", `SELECT U&"caf\00e9" FROM t`},
		{"a comment marker as data", "SELECT note FROM t WHERE note LIKE '--%'"},
		{"a name inside a literal is not a call", "SELECT 'pg_read_file' FROM t"},
		{"a parameter is not a dollar quote", "SELECT $1 FROM t WHERE id = $2"},
		{"a broken escape is left to postgres", `SELECT U&"set_confi\zzzz"('a', 'b', false)`},
	}
	for _, tt := range allowed {
		t.Run(tt.name, func(t *testing.T) {
			if err := guardRead(Defaults(), tt.sql); err != nil {
				t.Errorf("guardRead(%q) = %v, want it allowed", tt.sql, err)
			}
		})
	}
}

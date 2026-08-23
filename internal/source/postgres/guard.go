package postgres

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

// readableStatements start a statement a read tool will run. This is the third
// layer and the weakest: it exists so that a DELETE gets a clear refusal instead
// of the server's own message. The READ ONLY transaction is what actually holds
// (ADR-0001).
var readableStatements = []string{"select", "with", "table", "values", "show", "explain"}

// denyFunctions act outside the transactional model, which is exactly what READ
// ONLY does not stop; and a dev database is often reached as a superuser, so
// privileges do not stop them either (ADR-0001). Entries are globs matched
// against the called name, so a family closes in one line and stays closed when
// postgres adds to it; a family is globbed only where every member belongs on
// the list — pg_advisory_* would take the xact locks, which the rollback
// releases. Config only adds to the list.
var denyFunctions = []string{
	// Server files: pg_read_* is the two readers, pg_file_* and pg_logdir_ls are
	// adminpack's writers.
	"pg_read_*", "pg_stat_file", "pg_ls_*",
	"pg_file_*", "pg_logdir_ls",
	"lo_import", "lo_export",
	// dblink opens a libpq connection from the database host to any host:port,
	// and the error text carries the answer back. The bare name needs its own
	// entry: dblink_* does not match it.
	"dblink", "dblink_*",
	"pg_terminate_backend", "pg_cancel_backend",
	"pg_reload_conf", "pg_rotate_logfile", "pg_promote",
	// A session-level advisory lock outlives the rollback and goes back into the
	// pool held, blocking everyone who takes the same key.
	"pg_advisory_lock", "pg_advisory_lock_shared",
	"pg_try_advisory_lock", "pg_try_advisory_lock_shared",
	// Statistics do not come back once reset, and reading a logical slot
	// advances it, which breaks whoever replicates from it.
	"pg_stat_reset*",
	"pg_logical_slot_*", "pg_replication_origin_*",
	"pg_drop_replication_slot", "pg_replication_slot_advance",
	"pg_create_logical_replication_slot", "pg_create_physical_replication_slot",
	"pg_copy_logical_replication_slot", "pg_copy_physical_replication_slot",
	"pg_sync_replication_slots",
	// pg_backup_start/stop are named pg_start_backup/pg_stop_backup before 15,
	// and the README promises 14.
	"pg_switch_wal", "pg_create_restore_point",
	"pg_backup_start", "pg_backup_stop", "pg_start_backup", "pg_stop_backup",
	"pg_wal_replay_pause", "pg_wal_replay_resume",
	"pg_import_system_collations",
	// set_config is how a read statement turns off the statement_timeout the
	// lease just set on it.
	"set_config",
}

// guardRead is everything checked before a read statement reaches the server.
func guardRead(cfg Config, sql string) error {
	kw := firstKeyword(sql)
	if kw == "" {
		return &mcpsrv.Failure{Kind: mcpsrv.KindBadArgument, Detail: "no statement to run"}
	}
	if !slices.Contains(readableStatements, kw) {
		return &mcpsrv.Failure{
			Kind: mcpsrv.KindDenied,
			Detail: fmt.Sprintf("a read tool runs %s only; this statement starts with %q",
				strings.Join(readableStatements, ", "), kw),
			Hint: "a write tool of this server is the only way to change anything",
		}
	}
	scan := scannable(sql)
	if name, pattern := deniedCall(cfg, scan); name != "" {
		return &mcpsrv.Failure{
			Kind: mcpsrv.KindDenied,
			Detail: fmt.Sprintf("%s() has effects a read-only transaction does not undo, so it is on the deny list (%s)",
				name, pattern),
			Hint: "if this server should be allowed to call it, the deny list is not the place — that call is not a read",
		}
	}
	// COPY cannot start a readable statement and cannot be nested in one, so the
	// keyword check above already blocks it; the second lock is here because the
	// deny list is meant to hold on its own (ADR-0001).
	if copiesToProgram(scan) {
		return &mcpsrv.Failure{
			Kind:   mcpsrv.KindDenied,
			Detail: "COPY … TO/FROM PROGRAM runs a shell command on the database server",
		}
	}
	return nil
}

// firstKeyword is the first word of the statement, lowercased, with leading
// comments and parentheses skipped: "-- find users\n(SELECT …)" starts with
// select, and a model writes both.
func firstKeyword(sql string) string {
	s := strings.TrimLeftFunc(sql, unicode.IsSpace)
	for s != "" {
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			} else {
				s = ""
			}
		case strings.HasPrefix(s, "/*"):
			s = s[skipBlockComment(s):]
		case s[0] == '(':
			s = s[1:]
		default:
			end := strings.IndexFunc(s, func(r rune) bool { return !isIdentRune(r) })
			if end < 0 {
				end = len(s)
			}
			return strings.ToLower(s[:end])
		}
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
	}
	return ""
}

// skipBlockComment is the length of the comment starting at s. Postgres nests
// block comments, so the closing marker is found by counting, not by searching.
func skipBlockComment(s string) int {
	depth, i := 1, 2
	for depth > 0 && i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(s[i:], "*/"):
			depth--
			i += 2
		default:
			i++
		}
	}
	return min(i, len(s))
}

// scannable is the statement as the deny list must read it: lowercased, with
// comments replaced by a space and quoting marks dropped, because
// pg_read_file/**/() and "pg_read_file"() are both calls to it. A comment
// becomes a space rather than nothing — postgres ends the identifier there too.
func scannable(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				i = len(sql)
			} else {
				i += j
			}
			b.WriteByte(' ')
		case strings.HasPrefix(sql[i:], "/*"):
			i += skipBlockComment(sql[i:])
			b.WriteByte(' ')
		case sql[i] == '"':
			i++
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return strings.ToLower(b.String())
}

// String literals are left in on purpose: a name matched inside one costs a
// refusal on a legitimate query, while skipping the literal would let a call
// hide behind a quote.
func deniedCall(cfg Config, scan string) (name, pattern string) {
	patterns := slices.Concat(denyFunctions, cfg.Tools.Read.ExtraDenyFunctions)
	for _, call := range calledNames(scan) {
		for _, pat := range patterns {
			pat = strings.ToLower(strings.TrimSpace(pat))
			if ok, _ := path.Match(flatten(pat), flatten(call)); ok {
				return call, pat
			}
		}
	}
	return "", ""
}

// calledNames is every identifier sql applies to arguments: the bare name
// followed by "(". Requiring the parenthesis keeps a row that merely contains
// the word off the deny list, and a schema qualification does not hide the call
// — "." is not an identifier character, so pg_catalog.pg_ls_dir is scanned as
// pg_ls_dir. A call through a view or a function body is invisible.
func calledNames(sql string) []string {
	var names []string
	start := -1
	for i, r := range sql {
		if isIdentRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && applied(sql[i:]) {
			names = append(names, sql[start:i])
		}
		start = -1
	}
	return names
}

// applied reports whether the identifier that just ended is followed by its
// argument list. The scan runs on [scannable] output, where a comment is already
// a space, so pg_read_file/**/() is one call.
func applied(rest string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(rest, unicode.IsSpace), "(")
}

func copiesToProgram(scan string) bool {
	fields := strings.Fields(scan)
	if !slices.Contains(fields, "copy") || !slices.Contains(fields, "program") {
		return false
	}
	for i, f := range fields {
		if i > 0 && f == "program" && (fields[i-1] == "to" || fields[i-1] == "from") {
			return true
		}
	}
	return false
}

func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

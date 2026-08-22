package postgres

import (
	"fmt"
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
// privileges do not stop them either (ADR-0001). Config only adds to the list.
var denyFunctions = []string{
	"pg_terminate_backend", "pg_cancel_backend",
	"pg_read_file", "pg_read_binary_file", "pg_stat_file", "pg_ls_dir",
	"pg_reload_conf", "pg_rotate_logfile",
	"lo_import", "lo_export",
	"dblink", "dblink_exec", "dblink_open", "dblink_send_query",
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
	if name := deniedCall(cfg, sql); name != "" {
		return &mcpsrv.Failure{
			Kind:   mcpsrv.KindDenied,
			Detail: fmt.Sprintf("%s() has effects a read-only transaction does not undo, so it is on the deny list", name),
			Hint:   "if this server should be allowed to call it, the deny list is not the place — that call is not a read",
		}
	}
	// COPY cannot start a readable statement and cannot be nested in one, so the
	// keyword check above already blocks it; the second lock is here because the
	// deny list is meant to hold on its own (ADR-0001).
	if copiesToProgram(sql) {
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

func deniedCall(cfg Config, sql string) string {
	lower := strings.ToLower(sql)
	for _, name := range slices.Concat(denyFunctions, cfg.Tools.Read.ExtraDenyFunctions) {
		if calls(lower, strings.ToLower(strings.TrimSpace(name))) {
			return name
		}
	}
	return ""
}

// calls reports whether sql calls name: the bare identifier followed by "(".
// Requiring the parenthesis keeps a row that merely contains the word out of the
// deny list, and a schema qualification does not hide the call — "." is not an
// identifier character. A call through a view or a function body is invisible.
func calls(sql, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(sql); {
		j := strings.Index(sql[i:], name)
		if j < 0 {
			return false
		}
		at := i + j
		i = at + len(name)
		if at > 0 && isIdentRune(rune(sql[at-1])) {
			continue
		}
		if strings.HasPrefix(strings.TrimLeftFunc(sql[i:], unicode.IsSpace), "(") {
			return true
		}
	}
	return false
}

func copiesToProgram(sql string) bool {
	fields := strings.Fields(strings.ToLower(sql))
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

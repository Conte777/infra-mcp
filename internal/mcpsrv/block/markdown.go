package block

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Budget caps a rendered response; zero disables a limit, and whichever is reached first truncates.
type Budget struct {
	MaxRows int
	// MaxBytes bounds the whole response, block framing excepted: a table head or a code fence is emitted even when it does not fit.
	MaxBytes int
}

// limitKind names the cap that truncated a block; it goes into the notice, because it decides which way the model should narrow.
type limitKind string

const (
	noLimit   limitKind = ""
	rowLimit  limitKind = "maxRows"
	byteLimit limitKind = "maxBytes"
)

const blockSep = "\n"

// Markdown renders blocks into the text handed to the model, trimmed to fit bud.
func Markdown(blocks []Block, bud Budget) string {
	rem := bud.MaxBytes
	if rem <= 0 {
		rem = math.MaxInt
	} else {
		rem -= noticeReserve // the notice has to fit inside the budget it reports on
	}

	var parts, notices []string
	dropped := 0
	for i, b := range blocks {
		if rem <= 0 {
			dropped = len(blocks) - i
			break
		}
		if len(parts) > 0 {
			rem -= len(blockSep)
		}
		text, notice := renderBlock(b, bud.MaxRows, rem)
		rem -= len(text)
		if notice != "" {
			notices = append(notices, notice)
		}
		if text != "" {
			parts = append(parts, text)
		}
	}

	out := strings.Join(parts, blockSep)
	head := noticeHead(notices, dropped)
	if head == "" {
		return out
	}
	// The notice goes on top: from the bottom the model learns the output is partial only after paying for all of it.
	return head + "\n" + blockSep + out
}

// One notice covers the whole response: a line per truncated block would spend the budget on the report instead of the data.
func noticeHead(notices []string, dropped int) string {
	switch {
	case len(notices) == 0 && dropped == 0:
		return ""
	case len(notices) == 0:
		return droppedNotice(dropped)
	case len(notices) > 1 || dropped > 0:
		return notices[0] + moreTruncated
	default:
		return notices[0]
	}
}

func renderBlock(b Block, maxRows, limit int) (text, notice string) {
	switch v := b.(type) {
	case Table:
		return renderTable(v, maxRows, limit)
	case Code:
		return renderCode(v, limit)
	case KeyValues:
		return renderKeyValues(v, limit)
	case Text:
		return renderText(v, limit)
	}
	return "", ""
}

func renderTable(t Table, maxRows, limit int) (string, string) {
	if len(t.Columns) == 0 {
		return "", ""
	}

	rows := t.Rows
	kind := noLimit
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
		kind = rowLimit
	}

	head := tableHead(t.Columns)
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = tableRow(row, len(t.Columns))
	}

	total := max(t.Total, len(t.Rows))
	shown, notice := fitWithNotice(len(head), lines, limit, total, func(shown, total int) string {
		return rowsNotice(shown, total, byteLimit)
	})
	if notice == "" && kind == rowLimit {
		notice = rowsNotice(shown, total, rowLimit)
	}
	return head + strings.Join(lines[:shown], ""), notice
}

func renderCode(c Code, limit int) (string, string) {
	fence := fenceFor(c.Text)
	head := fence + c.Lang + "\n"
	tail := fence + "\n"

	lines := splitLines(c.Text)
	shown, notice := fitWithNotice(len(head)+len(tail), lines, limit, len(lines), linesNotice)
	return head + strings.Join(lines[:shown], "") + tail, notice
}

func renderKeyValues(kv KeyValues, limit int) (string, string) {
	lines := make([]string, len(kv))
	for i, p := range kv {
		lines[i] = escapeLine(p.Key) + ": " + escapeLine(p.Value) + "\n"
	}
	shown, notice := fitWithNotice(0, lines, limit, len(lines), entriesNotice)
	return strings.Join(lines[:shown], ""), notice
}

func renderText(t Text, limit int) (string, string) {
	lines := splitLines(string(t))
	shown, notice := fitWithNotice(0, lines, limit, len(lines), linesNotice)
	return strings.Join(lines[:shown], ""), notice
}

func fitWithNotice(fixed int, lines []string, limit, total int, notice func(shown, total int) string) (int, string) {
	shown := fit(fixed, lines, limit)
	if shown == len(lines) {
		return shown, ""
	}
	return shown, notice(shown, total)
}

// fit reports how many lines fit after fixed bytes of framing; the framing is emitted regardless, since a table without its header is unreadable.
func fit(fixed int, lines []string, limit int) int {
	used := fixed
	for i, l := range lines {
		if used+len(l) > limit {
			return i
		}
		used += len(l)
	}
	return len(lines)
}

const moreTruncated = " Later blocks of this response were cut or dropped for the same reason."

// Bytes held back from MaxBytes so the notice always fits; the bound is the widest notice this package can produce.
var noticeReserve = max(
	len(rowsNotice(math.MaxInt, math.MaxInt, byteLimit))+len(moreTruncated),
	len(droppedNotice(math.MaxInt)),
) + 2

func droppedNotice(dropped int) string {
	return fmt.Sprintf("%d further blocks of this response were dropped — the %s output budget was reached.", dropped, byteLimit)
}

// Every notice names the next move: a bare "truncated" invites the model to repeat the call and get the same cut.
func rowsNotice(shown, total int, kind limitKind) string {
	next := "add WHERE or aggregate"
	if kind == byteLimit {
		next = "select fewer columns or narrow the query"
	}
	return fmt.Sprintf("Showing %d of %d rows — the %s output budget was reached. Re-run and %s; no tool argument raises this limit.",
		shown, total, kind, next)
}

func linesNotice(shown, total int) string {
	return fmt.Sprintf("Showing the first %d of %d lines — the %s output budget was reached. Narrow the request; no tool argument raises this limit.",
		shown, total, byteLimit)
}

func entriesNotice(shown, total int) string {
	return fmt.Sprintf("Showing %d of %d entries — the %s output budget was reached.", shown, total, byteLimit)
}

// No padding: it costs +40% tokens and buys nothing, since a wide row is unreadable either way.
func tableHead(cols []string) string {
	var b strings.Builder
	b.WriteByte('|')
	for _, c := range cols {
		b.WriteString(escapeCell(c))
		b.WriteByte('|')
	}
	b.WriteString("\n|")
	b.WriteString(strings.Repeat("-|", len(cols)))
	b.WriteByte('\n')
	return b.String()
}

// Rows are forced rectangular against Columns: a ragged row would shift every cell after it with nothing to show for it.
func tableRow(row []any, cols int) string {
	var b strings.Builder
	b.WriteByte('|')
	for i := range cols {
		if i < len(row) {
			b.WriteString(escapeCell(cellText(row[i])))
		}
		b.WriteByte('|')
	}
	b.WriteByte('\n')
	return b.String()
}

// A newline in a value splits the row in two and the output still looks like valid markdown, so escaping is correctness, not polish.
var cellEscaper = strings.NewReplacer("|", "\\|", "\n", "\\n", "\r", "\\r")

// A lone backslash is left as is: escaping it taxes every path-like value to resolve an ambiguity that at worst mis-reports a newline.
func escapeCell(s string) string { return cellEscaper.Replace(s) }

var lineEscaper = strings.NewReplacer("\n", "\\n", "\r", "\\r")

func escapeLine(s string) string { return lineEscaper.Replace(s) }

func cellText(v any) string {
	switch t := v.(type) {
	case nil:
		return "" // NULL as an empty cell; the literal costs +17% tokens
	case string:
		return t
	case []byte:
		return fmt.Sprintf("\\x%x", t) // postgres bytea syntax, not a Go byte-slice dump
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// The fence has to outrun the longest backtick run inside, or a value with backticks closes the block early.
func fenceFor(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return strings.Repeat("`", max(3, longest+1))
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	parts := strings.SplitAfter(s, "\n")
	return parts[:len(parts)-1]
}

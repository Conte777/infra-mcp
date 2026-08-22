package block

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTableIsUnpaddedMarkdown(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id", "name", "note"},
		Rows: [][]any{
			{1, "a", nil},
			{22, "bbbb", nil},
		},
	}}, Budget{})

	want := "|id|name|note|\n|-|-|-|\n|1|a||\n|22|bbbb||\n"
	if out != want {
		t.Fatalf("Markdown() = %q, want %q", out, want)
	}
}

func TestTableKeepsColumnThatIsNullEverywhere(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id", "empty"},
		Rows:    [][]any{{1, nil}, {2, nil}},
	}}, Budget{})

	if !strings.HasPrefix(out, "|id|empty|\n") {
		t.Fatalf("all-NULL column dropped from the header: %q", out)
	}
}

func TestTableForcesRowsRectangular(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"a", "b", "c"},
		Rows:    [][]any{{1}, {1, 2, 3, 4}},
	}}, Budget{})

	want := "|a|b|c|\n|-|-|-|\n|1|||\n|1|2|3|\n"
	if out != want {
		t.Fatalf("Markdown() = %q, want %q", out, want)
	}
}

func TestTableEscapesCellsAndHeaders(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"a|b"},
		Rows:    [][]any{{"one\ntwo\r\nthree|four"}},
	}}, Budget{})

	want := "|a\\|b|\n|-|\n|one\\ntwo\\r\\nthree\\|four|\n"
	if out != want {
		t.Fatalf("Markdown() = %q, want %q", out, want)
	}
	if strings.Count(out, "\n") != 3 {
		t.Fatalf("a multi-line value broke the row apart: %q", out)
	}
}

func TestCellFormatting(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
	out := Markdown([]Block{Table{
		Columns: []string{"b", "raw", "at", "f"},
		Rows:    [][]any{{true, []byte{0xde, 0xad}, ts, 1.5}},
	}}, Budget{})

	want := "|b|raw|at|f|\n|-|-|-|-|\n|true|\\xdead|2026-08-21T10:30:00Z|1.5|\n"
	if out != want {
		t.Fatalf("Markdown() = %q, want %q", out, want)
	}
}

func TestMaxRowsTruncatesWithNoticeOnTop(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id"},
		Rows:    [][]any{{1}, {2}, {3}},
	}}, Budget{MaxRows: 2})

	if !strings.HasPrefix(out, "Showing 2 of 3 rows — the maxRows output budget was reached.") {
		t.Fatalf("notice missing or not on top: %q", out)
	}
	if !strings.HasSuffix(out, "|1|\n|2|\n") {
		t.Fatalf("wrong rows kept: %q", out)
	}
	if strings.Contains(out, "|3|") {
		t.Fatalf("row past maxRows rendered: %q", out)
	}
}

// A source that stopped reading has no total to name, and a notice that invents
// one ("of 201") reads as the whole answer.
func TestMoreSaysThereIsMoreWithoutATotal(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id"},
		Rows:    [][]any{{1}, {2}},
		More:    true,
	}}, Budget{MaxRows: 2})

	if !strings.HasPrefix(out, "Showing the first 2 rows and there are more") {
		t.Fatalf("notice missing or naming a total nobody counted: %q", out)
	}
	if strings.Contains(out, " of ") {
		t.Fatalf("notice invented a total: %q", out)
	}
}

func TestMoreIsReportedEvenWhenNothingWasCutHere(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id"},
		Rows:    [][]any{{1}},
		More:    true,
	}}, Budget{})

	if !strings.Contains(out, "there are more") {
		t.Fatalf("a source that stopped short went unreported: %q", out)
	}
}

func TestMaxRowsUsesTotalWhenSourceCounted(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id"},
		Rows:    [][]any{{1}, {2}},
		Total:   4524,
	}}, Budget{MaxRows: 1})

	if !strings.Contains(out, "Showing 1 of 4524 rows") {
		t.Fatalf("Total ignored in the notice: %q", out)
	}
}

func TestMaxBytesTruncatesWithinBudget(t *testing.T) {
	rows := make([][]any, 200)
	for i := range rows {
		rows[i] = []any{i, strings.Repeat("x", 40)}
	}
	const budget = 600

	out := Markdown([]Block{Table{Columns: []string{"id", "payload"}, Rows: rows}}, Budget{MaxBytes: budget})

	if len(out) > budget {
		t.Fatalf("output is %d bytes, over the %d budget", len(out), budget)
	}
	if !strings.HasPrefix(out, "Showing ") || !strings.Contains(out, "maxBytes output budget") {
		t.Fatalf("notice missing or not on top: %q", out)
	}
	shown := strings.Count(out, "|x")
	if shown == 0 || shown >= len(rows) {
		t.Fatalf("shown %d of %d rows, want a partial result", shown, len(rows))
	}
	if !strings.Contains(out, fmt.Sprintf("Showing %d of 200 rows", shown)) {
		t.Fatalf("notice count disagrees with the %d rows rendered: %q", shown, out)
	}
}

func TestBytesLimitWinsWhenBothWouldTruncate(t *testing.T) {
	rows := make([][]any, 50)
	for i := range rows {
		rows[i] = []any{strings.Repeat("y", 60)}
	}

	out := Markdown([]Block{Table{Columns: []string{"payload"}, Rows: rows}}, Budget{MaxRows: 40, MaxBytes: 500})

	if !strings.Contains(out, "maxBytes output budget") {
		t.Fatalf("want the byte limit named, got: %q", out)
	}
	if strings.Contains(out, "maxRows output budget") {
		t.Fatalf("both limits reported: %q", out)
	}
	if len(out) > 500 {
		t.Fatalf("output is %d bytes, over the 500 budget", len(out))
	}
}

func TestRowLimitWinsWhenBytesFit(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id"},
		Rows:    [][]any{{1}, {2}, {3}},
	}}, Budget{MaxRows: 1, MaxBytes: 4096})

	if !strings.Contains(out, "maxRows output budget") {
		t.Fatalf("want the row limit named, got: %q", out)
	}
}

func TestTableWithoutColumnsRendersNothing(t *testing.T) {
	if out := Markdown([]Block{Table{}}, Budget{}); out != "" {
		t.Fatalf("Markdown() = %q, want empty", out)
	}
}

func TestCodeFences(t *testing.T) {
	out := Markdown([]Block{Code{Lang: "sql", Text: "CREATE TABLE t (\n  id bigint\n);"}}, Budget{})

	want := "```sql\nCREATE TABLE t (\n  id bigint\n);\n```\n"
	if out != want {
		t.Fatalf("Markdown() = %q, want %q", out, want)
	}
}

func TestCodeFenceOutrunsBackticksInside(t *testing.T) {
	out := Markdown([]Block{Code{Text: "a ``` b ```` c"}}, Budget{})

	if !strings.HasPrefix(out, "`````\n") || !strings.HasSuffix(out, "\n`````\n") {
		t.Fatalf("fence does not enclose the backticks inside: %q", out)
	}
}

func TestCodeTruncatesFromTheTail(t *testing.T) {
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	const budget = 300

	out := Markdown([]Block{Code{Lang: "sql", Text: b.String()}}, Budget{MaxBytes: budget})

	if len(out) > budget {
		t.Fatalf("output is %d bytes, over the %d budget", len(out), budget)
	}
	if !strings.HasPrefix(out, "Showing the first ") {
		t.Fatalf("notice missing or not on top: %q", out)
	}
	if !strings.Contains(out, "line 0\n") || strings.Contains(out, "line 99\n") {
		t.Fatalf("want the head kept and the tail cut: %q", out)
	}
	if !strings.HasSuffix(out, "```\n") {
		t.Fatalf("truncation left the fence unclosed: %q", out)
	}
}

func TestKeyValuesAndText(t *testing.T) {
	out := Markdown([]Block{
		KeyValues{{Key: "profile", Value: "default"}, {Key: "config", Value: "/etc/x.json"}},
		Text("5 rows affected"),
	}, Budget{})

	want := "profile: default\nconfig: /etc/x.json\n\n5 rows affected\n"
	if out != want {
		t.Fatalf("Markdown() = %q, want %q", out, want)
	}
}

func TestKeyValuesEscapesNewlines(t *testing.T) {
	out := Markdown([]Block{KeyValues{{Key: "error", Value: "dial failed\nno route"}}}, Budget{})

	want := "error: dial failed\\nno route\n"
	if out != want {
		t.Fatalf("Markdown() = %q, want %q", out, want)
	}
}

func TestBudgetIsSharedAcrossBlocks(t *testing.T) {
	rows := make([][]any, 100)
	for i := range rows {
		rows[i] = []any{strings.Repeat("z", 30)}
	}
	const budget = 700

	out := Markdown([]Block{
		Table{Columns: []string{"payload"}, Rows: rows},
		Text("5 rows affected"),
	}, Budget{MaxBytes: budget})

	if len(out) > budget {
		t.Fatalf("output is %d bytes, over the %d budget", len(out), budget)
	}
	if !strings.HasSuffix(out, "5 rows affected\n") {
		t.Fatalf("the block after a truncated one was dropped: %q", out)
	}
}

func TestMaxCellCharsCutsTheValueNotTheTable(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"id", "payload"},
		Rows:    [][]any{{1, strings.Repeat("j", 50)}, {2, "short"}},
	}}, Budget{MaxCellChars: 10})

	if !strings.Contains(out, "|1|jjjjjjjjjj…|\n") {
		t.Fatalf("cell not cut to 10 characters: %q", out)
	}
	if !strings.Contains(out, "|2|short|\n") {
		t.Fatalf("a short value was touched: %q", out)
	}
	if !strings.Contains(out, "Values longer than 10 characters are cut") {
		t.Fatalf("a cut cell went unannounced: %q", out)
	}
	if strings.Contains(out, "rows — the") {
		t.Fatalf("cutting a cell must not report a row truncation: %q", out)
	}
}

func TestMaxCellCharsCutsOnRuneBoundary(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"s"},
		Rows:    [][]any{{"привет мир"}},
	}}, Budget{MaxCellChars: 6})

	if !strings.Contains(out, "|привет…|") {
		t.Fatalf("multibyte value cut wrongly: %q", out)
	}
}

// Escaping expands a newline into two characters; counting after it would charge the value for the escape.
func TestMaxCellCharsCountsTheValueNotItsEscape(t *testing.T) {
	out := Markdown([]Block{Table{
		Columns: []string{"s"},
		Rows:    [][]any{{"a\nbc"}},
	}}, Budget{MaxCellChars: 4})

	if !strings.Contains(out, "|a\\nbc|") {
		t.Fatalf("value of 4 characters was cut: %q", out)
	}
	if strings.Contains(out, "…") {
		t.Fatalf("value of 4 characters was cut: %q", out)
	}
}

// The point of the key: without it three blobs spend the byte budget the other rows needed.
func TestMaxCellCharsRunsBeforeMaxBytes(t *testing.T) {
	rows := make([][]any, 20)
	for i := range rows {
		rows[i] = []any{i, strings.Repeat("b", 400)}
	}
	blocks := []Block{Table{Columns: []string{"id", "blob"}, Rows: rows}}

	capped := Markdown(blocks, Budget{MaxBytes: 1200, MaxCellChars: 20})
	uncapped := Markdown(blocks, Budget{MaxBytes: 1200})

	if got := strings.Count(capped, "|bbb"); got != len(rows) {
		t.Fatalf("capped cells still cost rows: %d of %d shown", got, len(rows))
	}
	if got := strings.Count(uncapped, "|bbb"); got >= len(rows) {
		t.Fatalf("uncapped run was expected to lose rows to the blobs, got %d", got)
	}
}

func TestZeroMaxCellCharsLeavesValuesWhole(t *testing.T) {
	long := strings.Repeat("k", 500)
	out := Markdown([]Block{Table{Columns: []string{"s"}, Rows: [][]any{{long}}}}, Budget{})

	if !strings.Contains(out, long) {
		t.Fatalf("value cut without a cell limit: %q", out)
	}
}

func TestDroppedBlocksAreAnnounced(t *testing.T) {
	rows := make([][]any, 100)
	for i := range rows {
		rows[i] = []any{strings.Repeat("z", 30)}
	}

	out := Markdown([]Block{
		Table{Columns: []string{"payload"}, Rows: rows},
		Code{Lang: "sql", Text: "SELECT 1;"},
	}, Budget{MaxBytes: noticeReserve(Budget{}) + 10})

	if !strings.Contains(out, moreTruncated) {
		t.Fatalf("a dropped block went unannounced: %q", out)
	}
	if strings.Contains(out, "SELECT 1;") {
		t.Fatalf("the block was expected to be dropped, not rendered: %q", out)
	}
}

// The notice is itself output, so a budget that fits the data but not the report would silently blow past MaxBytes.
func TestOutputNeverExceedsBudget(t *testing.T) {
	rows := make([][]any, 60)
	for i := range rows {
		rows[i] = []any{i, strings.Repeat("q", 25)}
	}
	blocks := []Block{
		Table{Columns: []string{"id", "payload"}, Rows: rows, Total: 4524},
		Code{Lang: "sql", Text: strings.Repeat("SELECT 1;\n", 40)},
		KeyValues{{Key: "profile", Value: "default"}},
		Text("5 rows affected"),
	}

	for budget := 400; budget <= 4000; budget += 37 {
		for _, cell := range []int{0, 8} {
			bud := Budget{MaxBytes: budget, MaxCellChars: cell}
			if out := Markdown(blocks, bud); len(out) > budget {
				t.Fatalf("budget %d (maxCellChars %d) produced %d bytes", budget, cell, len(out))
			}
		}
	}
}

func TestNoBudgetRendersEverything(t *testing.T) {
	rows := make([][]any, 500)
	for i := range rows {
		rows[i] = []any{i}
	}

	out := Markdown([]Block{Table{Columns: []string{"id"}, Rows: rows}}, Budget{})

	if strings.Contains(out, "output budget was reached") {
		t.Fatalf("truncated without a budget: %q", out[:120])
	}
	if got := strings.Count(out, "\n"); got != len(rows)+2 {
		t.Fatalf("rendered %d lines, want %d", got, len(rows)+2)
	}
}

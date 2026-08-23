package postgres

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// The read-side cut is invisible in the rendered answer — capCell would have cut
// the same value to the same width — so what it has to guarantee is that enough
// of the value survives for that render to be exact.
func TestTextRowKeepsEveryRuneTheRendererCanShow(t *testing.T) {
	const maxCellChars = 5

	for _, tc := range []struct {
		name string
		r    string
	}{
		{"ascii", "a"},
		{"two-byte", "ä"},
		{"three-byte", "ы"},
		{"four-byte", "𝄞"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := strings.Repeat(tc.r, 100)
			cell, _ := textRow([][]byte{[]byte(value)}, maxCellChars)[0].(string)

			// Strictly more than the budget, not merely as many: capCell cuts on
			// "longer than", so a cell trimmed to exactly the budget renders as a
			// whole value and the ellipsis that says otherwise never appears.
			if got := utf8.RuneCountInString(cell); got <= maxCellChars {
				t.Errorf("kept %d runes of %q, and the renderer needs more than %d to see a cut", got, tc.r, maxCellChars)
			}
			// A cut mid-rune is allowed, and lands past the budget by construction;
			// what the renderer shows has to be the value's own opening runes.
			if got, want := head(cell, maxCellChars), head(value, maxCellChars); got != want {
				t.Errorf("the first %d runes are %q, want %q", maxCellChars, got, want)
			}
			if len(cell) > cellBytes(maxCellChars) {
				t.Errorf("kept %d bytes, over the %d the budget allows", len(cell), cellBytes(maxCellChars))
			}
		})
	}
}

// The read-side cut must stay invisible in the answer: what the model reads is
// the renderer's business, and it can only mark a value as cut if it was handed
// more of it than it shows.
func TestReadSideCutStillReachesTheModelAsACut(t *testing.T) {
	const maxCellChars = 5
	// Four bytes a rune is where the budget's byte allowance and its rune count
	// meet exactly, and an off-by-one there swallows the ellipsis.
	value := strings.Repeat("𝄞", 100)

	row := textRow([][]byte{[]byte(value)}, maxCellChars)
	out := block.Markdown([]block.Block{block.Table{Columns: []string{"v"}, Rows: [][]any{row}}}, block.Budget{MaxCellChars: maxCellChars})

	if !strings.Contains(out, "…") {
		t.Errorf("a cut value renders as a whole one: %q", out)
	}
	if !strings.Contains(out, "are cut and end with") {
		t.Errorf("the notice does not say values were cut: %q", out)
	}
}

func TestTextRowLeavesShortValuesWhole(t *testing.T) {
	const maxCellChars = 5
	value := strings.Repeat("𝄞", maxCellChars+1) // the whole allowance, at four bytes a rune

	if cell := textRow([][]byte{[]byte(value)}, maxCellChars)[0]; cell != value {
		t.Errorf("cell = %q, want the value untouched: %q", cell, value)
	}
}

// Zero is the renderer's "do not cut", and the two ends of the budget have to
// agree: a read-side cut here would trim a value nothing downstream trims.
func TestTextRowWithoutACellBudgetKeepsEverything(t *testing.T) {
	value := strings.Repeat("x", 10000)

	if cell := textRow([][]byte{[]byte(value)}, 0)[0]; cell != value {
		t.Errorf("cell is %d bytes, want the whole %d", len(cell.(string)), len(value))
	}
}

func TestTextRowKeepsNullApartFromEmpty(t *testing.T) {
	row := textRow([][]byte{nil, {}}, 10)

	if row[0] != nil {
		t.Errorf("NULL became %#v, and the renderer prints it as a value", row[0])
	}
	if row[1] != "" {
		t.Errorf("the empty value became %#v", row[1])
	}
}

func head(s string, runes int) string {
	return string([]rune(s)[:runes])
}

package postgres

import (
	"strings"
	"testing"
	"unicode/utf8"
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

			if got := utf8.RuneCountInString(cell); got < maxCellChars {
				t.Errorf("kept %d runes of %q, fewer than the %d the renderer will show", got, tc.r, maxCellChars)
			}
			// A cut mid-rune is allowed, and lands past the budget by construction;
			// what the renderer shows has to be the value's own opening runes.
			if got, want := head(cell, maxCellChars), head(value, maxCellChars); got != want {
				t.Errorf("the first %d runes are %q, want %q", maxCellChars, got, want)
			}
			if len(cell) > maxCellChars*maxRuneBytes {
				t.Errorf("kept %d bytes, over the %d the budget allows", len(cell), maxCellChars*maxRuneBytes)
			}
		})
	}
}

func TestTextRowLeavesShortValuesWhole(t *testing.T) {
	const maxCellChars = 5
	value := strings.Repeat("𝄞", maxCellChars) // exactly the budget, at four bytes a rune

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

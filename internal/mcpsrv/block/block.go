// Package block holds the closed dictionary of tool response blocks and the renderers that turn them into what the model reads.
package block

// Block is one unit of a tool response; the unexported marker closes the set, so no source can add a kind a renderer would drop.
type Block interface {
	isBlock()
}

// Table is a result set. A nil cell is NULL, and how a value is written out is the renderer's decision, not the source's.
type Table struct {
	Columns []string
	Rows    [][]any
	// Rows available before any budget; zero or less means the source did not count them.
	Total int
	// More says the source stopped reading with rows left: it cannot name a total, but "there is more" is still a fact the notice must carry.
	More bool
}

// Code is a fenced block: DDL, an EXPLAIN plan, a manifest, a log tail.
type Code struct {
	Lang string // fence info string ("sql", "yaml"); empty renders a plain fence
	Text string
}

// Pair is one entry of a KeyValues block.
type Pair struct {
	Key   string
	Value string
}

// KeyValues is a flat set of named values — the shape of `status`.
type KeyValues []Pair

// Text is prose: "5 rows affected", a note, an explained error.
type Text string

func (Table) isBlock()     {}
func (Code) isBlock()      {}
func (KeyValues) isBlock() {}
func (Text) isBlock()      {}

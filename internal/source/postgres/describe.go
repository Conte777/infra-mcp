package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// to_regclass resolves the name the way the SQL the model already wrote does —
// schema qualification, quoting and search_path included — and answers NULL
// instead of raising when nothing matches. A partition's parent and bounds are
// joined in, not fetched after: a join that finds nothing beats a round trip.
const relationSQL = `
SELECT c.oid,
       quote_ident(n.nspname) || '.' || quote_ident(c.relname),
       c.relkind::text,
       obj_description(c.oid, 'pg_class'),
       CASE WHEN c.relkind = 'p'
            THEN (SELECT pg_size_pretty(sum(pg_total_relation_size(t.relid)))
                    FROM pg_partition_tree(c.oid) t)
            WHEN c.relkind IN ('r','m')
            THEN pg_size_pretty(pg_total_relation_size(c.oid)) END,
       CASE WHEN c.relkind = 'p'
            THEN (SELECT CASE WHEN bool_and(pt.reltuples >= 0)
                              THEN sum(pt.reltuples)::bigint END
                    FROM pg_partition_tree(c.oid) t
                    JOIN pg_class pt ON pt.oid = t.relid
                   WHERE t.isleaf)
            ELSE NULLIF(c.reltuples, -1)::bigint END,
       CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) END,
       (SELECT count(*) FROM pg_inherits WHERE inhparent = c.oid),
       quote_ident(pn.nspname) || '.' || quote_ident(p.relname),
       pg_get_expr(c.relpartbound, c.oid)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_inherits inh ON inh.inhrelid = c.oid AND c.relispartition
LEFT JOIN pg_class p ON p.oid = inh.inhparent
LEFT JOIN pg_namespace pn ON pn.oid = p.relnamespace
WHERE c.oid = to_regclass($1::text)`

// attfdwoptions is empty on anything but a foreign table, so the per-column
// options ride along instead of costing a query of their own.
const columnsSQL = `
SELECT quote_ident(a.attname),
       format_type(a.atttypid, a.atttypmod),
       a.attnotnull,
       pg_get_expr(d.adbin, d.adrelid),
       a.attidentity::text,
       a.attgenerated::text,
       col_description(a.attrelid, a.attnum),
       a.attfdwoptions
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

// contype 'n' is left out: since 17 a NOT NULL is a catalog constraint too, and
// it is already printed on its column.
const constraintsSQL = `
SELECT quote_ident(conname), pg_get_constraintdef(oid)
FROM pg_constraint
WHERE conrelid = $1 AND contype IN ('p','u','f','c','x')
ORDER BY CASE contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'f' THEN 2 ELSE 3 END, conname`

// An index backing a constraint is printed as that constraint, not twice.
const indexesSQL = `
SELECT pg_get_indexdef(i.indexrelid)
FROM pg_index i
WHERE i.indrelid = $1
  AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid AND c.contype IN ('p','u','x'))
ORDER BY 1`

const referencesSQL = `
SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname), pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE con.confrelid = $1 AND con.contype = 'f'
ORDER BY 1, con.conname`

// The cast picks the overload: an untyped parameter makes this the form that
// takes a view name, and the oid arrives as its own digits.
const viewSQL = `SELECT pg_get_viewdef($1::oid, true)`

// Its own query, unlike the parent above: this one runs only for a foreign
// table, where a join would run for every relation. Server options are left out
// on purpose — the server is an object nobody asked about, and its options are
// the address of a system this tool gave no access to.
const foreignTableSQL = `
SELECT quote_ident(s.srvname), ft.ftoptions
FROM pg_foreign_table ft
JOIN pg_foreign_server s ON s.oid = ft.ftserver
WHERE ft.ftrelid = $1`

const partitionsSQL = `
SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname),
       pg_get_expr(c.relpartbound, c.oid)
FROM pg_inherits inh
JOIN pg_class c ON c.oid = inh.inhrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE inh.inhparent = $1
ORDER BY 1
LIMIT $2`

// Its own query, and only on the refusal path: naming the table an index sits
// on costs a round trip that every ordinary describe would pay for a line it
// never prints.
const indexTableSQL = `
SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE i.indexrelid = $1`

// A hard ceiling, not a config key: describe_table takes several names and the
// budget is one for the whole answer, so five hundred partitions would eat the
// DDL of the next table. Twenty already show the scheme (ADR-0004).
const maxPartitionsListed = 20

func describeTable(ctx context.Context, tx pgx.Tx, _ Config, in describeArgs) ([]block.Block, error) {
	if len(in.Tables) == 0 {
		return nil, &mcpsrv.Failure{Kind: mcpsrv.KindBadArgument, Detail: "name at least one table"}
	}

	blocks := make([]block.Block, 0, len(in.Tables))
	for _, name := range in.Tables {
		ddl, err := describeOne(ctx, tx, name)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block.Code{Lang: "sql", Text: ddl})
	}
	return blocks, nil
}

type relation struct {
	oid          uint32
	name         string
	kind         string
	comment      *string
	size         *string
	rows         *int64
	partitionKey *string
	partitions   int
	// parent and bound are set exactly for a partition; a partitioned table is
	// both when it sits under another one.
	parent *string
	bound  *string
}

func describeOne(ctx context.Context, tx pgx.Tx, name string) (string, error) {
	var rel relation
	err := tx.QueryRow(ctx, relationSQL, name).Scan(&rel.oid, &rel.name, &rel.kind,
		&rel.comment, &rel.size, &rel.rows, &rel.partitionKey, &rel.partitions,
		&rel.parent, &rel.bound)
	// to_regclass answers NULL for a name that matches nothing, but raises for
	// one that is not a name at all: "" is a syntax error (42), and the
	// db.schema.table a model reaches for when it qualifies too far is an
	// unimplemented cross-database reference (0A). All three are one mistake.
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) ||
		(errors.As(err, &pgErr) && (class(pgErr.Code) == "42" || class(pgErr.Code) == "0A")) {
		return "", &mcpsrv.Failure{
			Kind:   mcpsrv.KindBadArgument,
			Detail: fmt.Sprintf("no table, view, materialized view, partitioned or foreign table named %q", name),
			Hint:   "list the tables first, or qualify the name with its schema",
			Err:    err,
		}
	}
	if err != nil {
		return "", err
	}

	if !slices.Contains(describableKinds, rel.kind) {
		return "", refuseKind(ctx, tx, rel)
	}

	var b strings.Builder
	b.WriteString(headline(rel))
	if rel.kind == "v" || rel.kind == "m" {
		if err := writeView(ctx, tx, &b, rel); err != nil {
			return "", err
		}
	} else if err := writeTable(ctx, tx, &b, rel); err != nil {
		return "", err
	}
	if err := writeIndexes(ctx, tx, &b, rel); err != nil {
		return "", err
	}
	if err := writePartitions(ctx, tx, &b, rel); err != nil {
		return "", err
	}
	if err := writeReferences(ctx, tx, &b, rel); err != nil {
		return "", err
	}
	return b.String(), nil
}

func headline(rel relation) string {
	parts := []string{kindLabel(rel)}
	if rel.rows != nil {
		parts = append(parts, fmt.Sprintf("~%d rows", *rel.rows))
	}
	if size := sizeLabel(rel); size != "" {
		parts = append(parts, size)
	}

	out := commentLine("%s: %s", rel.name, strings.Join(parts, ", "))
	// The bounds go on their own line: a list partition's are a hundred values
	// long, and the first line has to read the same for every kind.
	if rel.bound != nil {
		out += commentLine("%s", *rel.bound)
	}
	if rel.comment != nil {
		out += commentLine("%s", *rel.comment)
	}
	return out
}

// A partition says whose it is instead of what relkind calls it; one that is
// partitioned itself still ends up carrying both facts, the other being the
// PARTITION BY after its body.
func kindLabel(rel relation) string {
	if rel.parent != nil {
		return "partition of " + *rel.parent
	}
	return kindName(rel.kind)
}

// "across N partitions" is what says the numbers cover the parts: a partitioned
// table's own storage is empty by definition, so both are summed over its tree.
func sizeLabel(rel relation) string {
	var size string
	if rel.size != nil {
		size = *rel.size
	}
	if rel.kind != "p" {
		return size
	}

	across := "across " + partitionCount(rel.partitions)
	if size == "" {
		return across
	}
	return size + " " + across
}

// Exactly the set listTablesSQL shows (catalog.go): "show me the tables" and
// "describe this table" answer to one definition of table-like, and a relkind
// postgres adds later is refused rather than printed as whatever it resembles.
var describableKinds = []string{"r", "v", "m", "p", "f"}

// kindName names every relkind, describable or not: the headline and the
// refusal read the same list, so nothing can be a table in one and something
// else in the other. An unlisted kind is named by its raw letter — it costs
// nothing and makes the next bug report unambiguous.
func kindName(relkind string) string {
	switch relkind {
	case "r":
		return "table"
	case "v":
		return "view"
	case "m":
		return "materialized view"
	case "p":
		return "partitioned table"
	case "f":
		return "foreign table"
	case "i", "I":
		return "index"
	case "S":
		return "sequence"
	case "c":
		return "composite type"
	case "t":
		return "TOAST table"
	}
	return "relkind " + relkind
}

// The refusal is an answer too, and it is paid for in the same tokens as a line
// of DDL — so it names what the object turned out to be instead of costing a
// round trip to find out.
func refuseKind(ctx context.Context, tx pgx.Tx, rel relation) error {
	kind := kindName(rel.kind)
	f := &mcpsrv.Failure{
		Kind:   mcpsrv.KindBadArgument,
		Detail: oneLine(fmt.Sprintf("%s is %s %s, not a table", rel.name, article(kind), kind)),
	}

	switch rel.kind {
	case "S":
		f.Hint = "its parameters are in pg_sequences"
	case "i", "I":
		// A hintless refusal beats the internal error the raw scan error would
		// become: the index can be dropped between the two queries, and the
		// answer that matters is already built.
		var table string
		if err := tx.QueryRow(ctx, indexTableSQL, rel.oid).Scan(&table); err == nil {
			f.Hint = oneLine(fmt.Sprintf("it indexes %s, whose description already prints it", table))
		}
	}
	return f
}

// "index" is the only kind that takes "an" today; spelling out the rule rather
// than the exception is what keeps the kind added next from reading wrong.
func article(noun string) string {
	if noun == "" || !strings.ContainsRune("aeiou", rune(noun[0])) {
		return "a"
	}
	return "an"
}

func writeView(ctx context.Context, tx pgx.Tx, b *strings.Builder, rel relation) error {
	var def string
	if err := tx.QueryRow(ctx, viewSQL, rel.oid).Scan(&def); err != nil {
		return err
	}
	word := "VIEW"
	if rel.kind == "m" {
		word = "MATERIALIZED VIEW"
	}
	b.WriteString(ddlLine("CREATE %s %s AS", word, rel.name))
	// The one multi-line thing here, and the only text not written a line at a
	// time: it is the view's own SQL, and folding it up would be its own lie.
	fmt.Fprintf(b, "%s\n", strings.TrimRight(def, "\n"))
	return nil
}

func writeTable(ctx context.Context, tx pgx.Tx, b *strings.Builder, rel relation) error {
	cols, err := columnLines(ctx, tx, rel.oid)
	if err != nil {
		return err
	}
	constraints, err := constraintLines(ctx, tx, rel.oid)
	if err != nil {
		return err
	}

	word := "TABLE"
	if rel.kind == "f" {
		word = "FOREIGN TABLE"
	}
	b.WriteString(ddlLine("CREATE %s %s (", word, rel.name))
	body := append(cols, constraints...)
	for i, line := range body {
		text := line.text
		// The comma belongs between the definition and its comment, so the
		// separator cannot be part of either.
		if i < len(body)-1 {
			text += ","
		}
		if line.comment != "" {
			text += " -- " + line.comment
		}
		b.WriteString(ddlLine("%s", text))
	}

	tail := ")"
	if rel.partitionKey != nil {
		tail += " PARTITION BY " + *rel.partitionKey
	}
	if rel.kind == "f" {
		clause, err := foreignClause(ctx, tx, rel.oid)
		if err != nil {
			return err
		}
		tail += clause
	}
	b.WriteString(ddlLine("%s;", tail))
	return nil
}

func foreignClause(ctx context.Context, tx pgx.Tx, oid uint32) (string, error) {
	var server string
	var options []string
	if err := tx.QueryRow(ctx, foreignTableSQL, oid).Scan(&server, &options); err != nil {
		return "", err
	}
	return " SERVER " + server + optionList(options), nil
}

// optionList turns the catalog's "name=value" array into the OPTIONS clause
// postgres itself takes; the value is a literal, so its quotes double. Both
// halves ride out on a ddlLine, which is where they are folded to one line.
func optionList(options []string) string {
	if len(options) == 0 {
		return ""
	}
	parts := make([]string, 0, len(options))
	for _, option := range options {
		name, value, _ := strings.Cut(option, "=")
		parts = append(parts, name+" '"+strings.ReplaceAll(value, "'", "''")+"'")
	}
	return " OPTIONS (" + strings.Join(parts, ", ") + ")"
}

// bodyLine is one line inside CREATE TABLE (…), kept apart from its trailing
// comment so the comma can go between them.
type bodyLine struct {
	text    string
	comment string
}

func columnLines(ctx context.Context, tx pgx.Tx, oid uint32) ([]bodyLine, error) {
	rows, err := tx.Query(ctx, columnsSQL, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []bodyLine
	for rows.Next() {
		var name, dataType string
		var notNull bool
		var def, comment *string
		var identity, generated string
		var options []string
		if err := rows.Scan(&name, &dataType, &notNull, &def, &identity, &generated, &comment, &options); err != nil {
			return nil, err
		}

		// OPTIONS sits between the type and the constraints, where the grammar
		// for a foreign table's column puts it.
		line := bodyLine{text: "  " + name + " " + dataType + optionList(options)}
		switch {
		case generated == "s" && def != nil:
			line.text += " GENERATED ALWAYS AS (" + *def + ") STORED"
		case identity == "a":
			line.text += " GENERATED ALWAYS AS IDENTITY"
		case identity == "d":
			line.text += " GENERATED BY DEFAULT AS IDENTITY"
		case def != nil:
			line.text += " DEFAULT " + *def
		}
		if notNull {
			line.text += " NOT NULL"
		}
		if comment != nil {
			line.comment = *comment
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

func constraintLines(ctx context.Context, tx pgx.Tx, oid uint32) ([]bodyLine, error) {
	rows, err := tx.Query(ctx, constraintsSQL, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []bodyLine
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			return nil, err
		}
		lines = append(lines, bodyLine{text: "  CONSTRAINT " + name + " " + def})
	}
	return lines, rows.Err()
}

func writeIndexes(ctx context.Context, tx pgx.Tx, b *strings.Builder, rel relation) error {
	rows, err := tx.Query(ctx, indexesSQL, rel.oid)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			return err
		}
		b.WriteString(ddlLine("%s;", def))
	}
	return rows.Err()
}

// Each partition names itself on its own line rather than hanging off a header:
// the budget can cut the list anywhere, and an orphaned indented line says
// nothing about what it is.
func writePartitions(ctx context.Context, tx pgx.Tx, b *strings.Builder, rel relation) error {
	if rel.kind != "p" {
		return nil
	}

	rows, err := tx.Query(ctx, partitionsSQL, rel.oid, maxPartitionsListed)
	if err != nil {
		return err
	}
	defer rows.Close()

	listed := 0
	for rows.Next() {
		var name, bound string
		if err := rows.Scan(&name, &bound); err != nil {
			return err
		}
		b.WriteString(commentLine("partition %s %s", name, bound))
		listed++
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if rest := rel.partitions - listed; rest > 0 {
		b.WriteString(commentLine("and %s not listed", partitionCount(rest)))
	}
	return nil
}

// Inbound foreign keys are comments: they are not this table's DDL, but without
// them nothing says which tables break if a row here is deleted.
func writeReferences(ctx context.Context, tx pgx.Tx, b *strings.Builder, rel relation) error {
	rows, err := tx.Query(ctx, referencesSQL, rel.oid)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var from, def string
		if err := rows.Scan(&from, &def); err != nil {
			return err
		}
		b.WriteString(commentLine("referenced by %s: %s", from, def))
	}
	return rows.Err()
}

// ddlLine is the only way a line of this answer is written, the view body
// excepted: every line is made of catalog strings, and a newline in any of them
// forges a line the catalog never held. A quoted identifier may legally carry
// one, and quote_ident does not strip it.
func ddlLine(format string, args ...any) string {
	return oneLine(fmt.Sprintf(format, args...)) + "\n"
}

func commentLine(format string, args ...any) string {
	return ddlLine("-- "+format, args...)
}

// oneLine is called where a line of the answer is written and nowhere else —
// ddlLine, and the refusal text, which is a line of the answer as much as any
// DDL is. Sanitising a field at the place it is read is how the last hole was
// left.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// partitionCount says how many parts there are, in one voice for the header and
// for the tail of the list.
func partitionCount(n int) string {
	if n == 1 {
		return "1 partition"
	}
	return fmt.Sprintf("%d partitions", n)
}

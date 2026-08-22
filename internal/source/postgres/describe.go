package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// to_regclass resolves the name the way the SQL the model already wrote does —
// schema qualification, quoting and search_path included — and answers NULL
// instead of raising when nothing matches.
const relationSQL = `
SELECT c.oid,
       quote_ident(n.nspname) || '.' || quote_ident(c.relname),
       c.relkind::text,
       obj_description(c.oid, 'pg_class'),
       CASE WHEN c.relkind IN ('r','m','p') THEN pg_size_pretty(pg_total_relation_size(c.oid)) END,
       NULLIF(c.reltuples, -1)::bigint,
       CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) END,
       (SELECT count(*) FROM pg_inherits WHERE inhparent = c.oid)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.oid = to_regclass($1::text)`

const columnsSQL = `
SELECT quote_ident(a.attname),
       format_type(a.atttypid, a.atttypmod),
       a.attnotnull,
       pg_get_expr(d.adbin, d.adrelid),
       a.attidentity::text,
       a.attgenerated::text,
       col_description(a.attrelid, a.attnum)
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
}

func describeOne(ctx context.Context, tx pgx.Tx, name string) (string, error) {
	var rel relation
	err := tx.QueryRow(ctx, relationSQL, name).Scan(&rel.oid, &rel.name, &rel.kind,
		&rel.comment, &rel.size, &rel.rows, &rel.partitionKey, &rel.partitions)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &mcpsrv.Failure{
			Kind:   mcpsrv.KindBadArgument,
			Detail: fmt.Sprintf("no table, view or materialized view named %q", name),
			Hint:   "list the tables first, or qualify the name with its schema",
		}
	}
	if err != nil {
		return "", err
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
	if err := writeReferences(ctx, tx, &b, rel); err != nil {
		return "", err
	}
	return b.String(), nil
}

func headline(rel relation) string {
	parts := []string{kindName(rel.kind)}
	if rel.rows != nil {
		parts = append(parts, fmt.Sprintf("~%d rows", *rel.rows))
	}
	if rel.size != nil {
		parts = append(parts, *rel.size)
	}
	if rel.partitionKey != nil {
		parts = append(parts, fmt.Sprintf("%d partitions", rel.partitions))
	}

	out := fmt.Sprintf("-- %s: %s\n", rel.name, strings.Join(parts, ", "))
	if rel.comment != nil {
		out += "-- " + oneLine(*rel.comment) + "\n"
	}
	return out
}

func kindName(relkind string) string {
	switch relkind {
	case "v":
		return "view"
	case "m":
		return "materialized view"
	case "p":
		return "partitioned table"
	case "f":
		return "foreign table"
	default:
		return "table"
	}
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
	fmt.Fprintf(b, "CREATE %s %s AS\n%s\n", word, rel.name, strings.TrimRight(def, "\n"))
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

	fmt.Fprintf(b, "CREATE TABLE %s (\n", rel.name)
	body := append(cols, constraints...)
	for i, line := range body {
		b.WriteString(line.text)
		// The comma belongs between the definition and its comment, so the
		// separator cannot be part of either.
		if i < len(body)-1 {
			b.WriteString(",")
		}
		if line.comment != "" {
			b.WriteString(" -- ")
			b.WriteString(line.comment)
		}
		b.WriteString("\n")
	}
	b.WriteString(")")
	if rel.partitionKey != nil {
		fmt.Fprintf(b, " PARTITION BY %s", *rel.partitionKey)
	}
	b.WriteString(";\n")
	return nil
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
		if err := rows.Scan(&name, &dataType, &notNull, &def, &identity, &generated, &comment); err != nil {
			return nil, err
		}

		line := bodyLine{text: "  " + name + " " + dataType}
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
			line.comment = oneLine(*comment)
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
		b.WriteString(def)
		b.WriteString(";\n")
	}
	return rows.Err()
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
		fmt.Fprintf(b, "-- referenced by %s: %s\n", from, def)
	}
	return rows.Err()
}

// oneLine keeps a comment inside its "--": a newline in it would turn the rest
// of the comment into DDL.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

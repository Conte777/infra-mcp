package postgres

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// cursorName is fixed: the transaction owns one connection, and it closes the
// cursor when it ends.
const cursorName = "infra_mcp_query"

func runQuery(ctx context.Context, tx pgx.Tx, cfg Config, in sqlArgs) ([]block.Block, error) {
	if err := guardRead(cfg, in.SQL); err != nil {
		return nil, err
	}

	sql := trimStatement(in.SQL)
	limit := cfg.Output.MaxRows
	// A cursor is what keeps "SELECT * FROM huge_table" from being a mistake: a
	// plain query streams every row to us to be thrown away, and the statement
	// timeout fires before the first ones can be shown. SHOW and EXPLAIN cannot
	// be declared as one, and their answers are small anyway.
	if limit > 0 && cursorable(sql) {
		// Not tx.Exec: pgx forces the simple protocol on an argument-less Exec,
		// where "SELECT 1; COMMIT; DROP TABLE t" is three statements and the
		// COMMIT ends the READ ONLY transaction. ExecParams carries one statement
		// by protocol, so postgres refuses the tail itself.
		decl := "DECLARE " + cursorName + " NO SCROLL CURSOR FOR " + sql
		if _, err := tx.Conn().PgConn().ExecParams(ctx, decl, nil, nil, nil, nil).Close(); err != nil {
			return nil, err
		}
		// One row past the budget, so that "there is more" is a fact and not a guess.
		sql = "FETCH FORWARD " + strconv.Itoa(limit+1) + " FROM " + cursorName
	}

	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	t := block.Table{Columns: columnNames(rows.FieldDescriptions())}
	for rows.Next() {
		t.Rows = append(t.Rows, textRow(rows.RawValues()))
		if limit > 0 && len(t.Rows) > limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(t.Rows) > limit {
		t.Rows = t.Rows[:limit]
		t.More = true
	} else {
		t.Total = len(t.Rows)
	}
	if len(t.Columns) == 0 {
		return []block.Block{block.Text("the statement returned no columns")}, nil
	}
	return []block.Block{t}, nil
}

// cursorable reports whether DECLARE CURSOR accepts the statement: it takes a
// query, which SHOW and EXPLAIN are not.
func cursorable(sql string) bool {
	switch firstKeyword(sql) {
	case "select", "with", "table", "values":
		return true
	default:
		return false
	}
}

func explain(ctx context.Context, tx pgx.Tx, cfg Config, in explainArgs) ([]block.Block, error) {
	if err := guardRead(cfg, in.SQL); err != nil {
		return nil, err
	}

	options := "COSTS"
	if in.Analyze {
		options = "ANALYZE, BUFFERS"
	}

	rows, err := tx.Query(ctx, "EXPLAIN ("+options+") "+trimStatement(in.SQL))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return []block.Block{block.Code{Lang: "text", Text: plan.String()}}, nil
}

// execute goes through PgConn rather than tx.Exec: it is the same simple
// protocol pgx uses for an argument-less Exec, but every statement's command tag
// and every RETURNING row comes back, not only the last statement's tag.
func execute(ctx context.Context, tx pgx.Tx, cfg Config, in sqlArgs) ([]block.Block, error) {
	conn := tx.Conn().PgConn()
	mrr := conn.Exec(ctx, in.SQL)
	// Closed on every path, not only the happy one: the connection stays locked
	// until ReadyForQuery is read, and the rollback that follows a failed script
	// would then fail as well and take the pooled connection down with it.
	defer func() { _ = mrr.Close() }()

	var blocks []block.Block
	var tags []string
	for mrr.NextResult() {
		rr := mrr.ResultReader()
		if t, ok := returnedRows(rr, cfg.Output.MaxRows); ok {
			blocks = append(blocks, t)
		}
		tag, err := rr.Close()
		if err != nil {
			return nil, err
		}
		// An empty statement — "" or nothing but a comment — still produces a
		// result, with no tag on it.
		if s := tag.String(); s != "" {
			tags = append(tags, s)
		}
	}
	if err := mrr.Close(); err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, &mcpsrv.Failure{Kind: mcpsrv.KindBadArgument, Detail: "no statement to run"}
	}

	// A COMMIT or ROLLBACK inside the script ends the transaction this tool
	// opened; postgres answers the commit that follows with a warning, so
	// nothing else would report that the script was not atomic after all. The
	// status cannot tell the two apart — both leave 'I' — so the notice must not
	// claim the work landed.
	if conn.TxStatus() != 'T' {
		blocks = append(blocks, block.Text(
			"this script ended the transaction itself: everything before that COMMIT or ROLLBACK was decided by it, "+
				"and everything after ran outside the transaction and committed on its own"))
	}
	return append(blocks, block.Text(strings.Join(tags, "\n"))), nil
}

// returnedRows drains one result, collecting what RETURNING produced. The rows
// exist whether or not they are read, so the cap is on what is kept, not on what
// is read: the tag comes only after the last row.
func returnedRows(rr *pgconn.ResultReader, limit int) (block.Table, bool) {
	fields := rr.FieldDescriptions()
	if len(fields) == 0 {
		return block.Table{}, false
	}

	t := block.Table{Columns: columnNames(fields)}
	read := 0
	for rr.NextRow() {
		read++
		if limit <= 0 || len(t.Rows) <= limit {
			t.Rows = append(t.Rows, textRow(rr.Values()))
		}
	}
	t.Total = read
	return t, true
}

func columnNames(fields []pgconn.FieldDescription) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

// textRow copies one row of raw values into cells. Both paths ask postgres for
// the text format — pgx leaves the result formats empty under QueryExecModeExec,
// and the simple protocol has no other — so a value reads exactly as postgres
// writes it. The copy is required: the driver reuses the buffer.
func textRow(raw [][]byte) []any {
	row := make([]any, len(raw))
	for i, v := range raw {
		if v == nil {
			continue // NULL, and the renderer's empty cell
		}
		row[i] = string(v)
	}
	return row
}

// trimStatement drops the trailing semicolon: the statement is embedded into
// DECLARE CURSOR and EXPLAIN, where it would end the outer statement instead.
func trimStatement(sql string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
}

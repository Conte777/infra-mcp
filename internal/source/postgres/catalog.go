package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// pg_database_size refuses a database the role cannot connect to, so the
// privilege is checked in the same row rather than after the fact.
const listDatabasesSQL = `
SELECT d.datname,
       CASE WHEN has_database_privilege(d.oid, 'CONNECT')
            THEN pg_size_pretty(pg_database_size(d.oid)) END,
       d.datallowconn,
       shobj_description(d.oid, 'pg_database')
FROM pg_database d
WHERE NOT d.datistemplate
ORDER BY d.datname`

func listDatabases(ctx context.Context, tx pgx.Tx, cfg Config, _ ConnectionArgs) ([]block.Block, error) {
	rows, err := tx.Query(ctx, listDatabasesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// The budget is spent after the filter, not in a LIMIT: which databases are
	// reachable is decided here in Go, so a SQL limit would cut by name and leave
	// include: ["orders"] showing nothing on a cluster with enough databases.
	limit := cfg.Output.MaxRows
	t := block.Table{Columns: []string{"database", "size", "connectable", "comment"}}
	for rows.Next() {
		var name string
		var size, comment *string
		var allowsConnections bool
		if err := rows.Scan(&name, &size, &allowsConnections, &comment); err != nil {
			return nil, err
		}
		// Listed exactly when reachable: the same call the tools resolve their
		// database argument through decides both — including for the database
		// this query is running in, which is an entry point and not a licence.
		if _, err := resolveDatabase(cfg, name, true); err != nil {
			continue
		}
		t.Rows = append(t.Rows, []any{name, value(size), allowsConnections, value(comment)})
		if limit > 0 && len(t.Rows) > limit { // one row past the budget, as the cursor does
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	t.Cap(limit)
	return []block.Block{t}, nil
}

// Partitions are left out: a partitioned table with a hundred children would
// otherwise bury the tables somebody asked about. pg_total_relation_size counts
// only the parent's own storage, so a partitioned table reads as small.
const listTablesSQL = `
SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname),
       CASE c.relkind WHEN 'r' THEN 'table' WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview'
                      WHEN 'p' THEN 'partitioned' WHEN 'f' THEN 'foreign' END,
       NULLIF(c.reltuples, -1)::bigint,
       CASE WHEN c.relkind IN ('r','m','p') THEN pg_size_pretty(pg_total_relation_size(c.oid)) END,
       obj_description(c.oid, 'pg_class')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r','v','m','p','f')
  AND NOT c.relispartition
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT LIKE 'pg_temp%'
  AND ($1::text = '' OR c.relname ILIKE $1::text OR n.nspname || '.' || c.relname ILIKE $1::text)
ORDER BY 1
LIMIT $2::bigint`

func listTables(ctx context.Context, tx pgx.Tx, cfg Config, in listTablesArgs) ([]block.Block, error) {
	// Nothing filters these rows afterwards, so the budget belongs in the
	// statement: a schema-per-tenant cluster would otherwise stream the whole of
	// pg_class here to be thrown away. A NULL limit is postgres for "all of them".
	limit := cfg.Output.MaxRows
	var fetch *int64
	if limit > 0 {
		n := int64(limit) + 1 // one row past the budget, as the cursor does
		fetch = &n
	}

	rows, err := tx.Query(ctx, listTablesSQL, in.Pattern, fetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	t := block.Table{Columns: []string{"table", "kind", "rows", "size", "comment"}}
	for rows.Next() {
		var name string
		var kind, size, comment *string
		var estimate *int64
		if err := rows.Scan(&name, &kind, &estimate, &size, &comment); err != nil {
			return nil, err
		}
		t.Rows = append(t.Rows, []any{name, value(kind), value(estimate), value(size), value(comment)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	t.Cap(limit)
	return []block.Block{t}, nil
}

// value unwraps a nullable column into a cell. A typed nil pointer is not the
// renderer's nil, and would print as "<nil>" instead of an empty cell.
func value[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

package postgres

import "github.com/Conte777/infra-mcp/internal/mcpsrv"

// The action half of a tool name; the core assembles the rest, so no full tool
// name is written out anywhere (ADR-0003).
const (
	actionListDatabases = "list_databases"
	actionListTables    = "list_tables"
	actionDescribeTable = "describe_table"
	actionQuery         = "query"
	actionExplain       = "explain"
	actionExecute       = "execute"
)

// Descriptions are paid for on every request, and the database list is not
// repeated in them: it lives in list_databases alone (ADR-0001).
const (
	descListDatabases = "List the databases the tools may reach in one cluster, with their size and whether connections are allowed."
	descListTables    = "List the tables, views, materialized views and foreign tables of one database, with estimated row counts and total size. Partitions of a partitioned table are left out."
	descDescribeTable = "Show one or more tables in CREATE TABLE form: columns, defaults, keys, indexes, checks, comments, and the foreign keys pointing back at them. Written to be read, not executed."
	descQuery         = "Run one read-only statement (SELECT, WITH, TABLE, VALUES, SHOW, EXPLAIN) and return its rows. Narrow with WHERE and LIMIT: the answer is cut to a configured budget that no argument raises."
	descExplain       = "Show the query plan for one read-only statement."
	descExecute       = "Run statements that change the database. All of them run in one transaction, committed only if every one succeeds, so do not write BEGIN or COMMIT."
)

// listTablesArgs narrows by name, which is what the model cannot do for itself
// here — unlike a query, where LIMIT is already at hand (ADR-0001).
type listTablesArgs struct {
	Args
	Pattern string `json:"pattern,omitzero" jsonschema:"SQL LIKE pattern over the table or schema.table name, case-insensitive, e.g. order%"`
}

type describeArgs struct {
	Args
	Tables []string `json:"tables" jsonschema:"table names, optionally schema-qualified; an unqualified name resolves through search_path"`
}

type sqlArgs struct {
	Args
	SQL string `json:"sql" jsonschema:"the statement to run"`
}

type explainArgs struct {
	Args
	SQL     string `json:"sql" jsonschema:"the statement to plan"`
	Analyze bool   `json:"analyze,omitzero" jsonschema:"run the statement to measure it, instead of only estimating; safe on data, because the statement still runs read-only"`
}

func registerTools(s *Source, r *mcpsrv.Registry[Config]) {
	read(s, r, actionListDatabases, descListDatabases, listDatabases)
	read(s, r, actionListTables, descListTables, listTables)
	read(s, r, actionDescribeTable, descDescribeTable, describeTable)
	read(s, r, actionQuery, descQuery, runQuery)
	read(s, r, actionExplain, descExplain, explain)
	write(s, r, actionExecute, descExecute, execute)
}

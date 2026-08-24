# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). A
section exists only for a released version — the tag the plugin manifests name —
and is written by the pull request that bumps them, so there is no `Unreleased`
heading to keep in sync (ADR-0005).

## [0.2.0] - 2026-08-24

### Breaking

- **One config file holds every environment and cluster.** The top-level
  `connection` of an 0.1 config is gone: every cluster lives under
  `environments.<environment>.clusters.<cluster>`, and the levels above hold
  only what those clusters inherit. A file still in the old shape is refused at
  startup by a message naming the replacement, not by a heap of unknown-key
  complaints.
- **The config file is `infra-mcp/postgres.json`**, not
  `infra-mcp/postgres.default.json`: `--profile` is gone along with the profile
  itself, and one server now reaches every environment. The search order is
  `--config`, then `INFRA_MCP_POSTGRES_CONFIG`, then XDG.
- **Every tool call names an address.** `environment`, `cluster` and `database`
  are three required arguments with no defaults: a single-environment config no
  longer starts answering calls that omit it, and none of them will start
  answering differently when a second cluster is added.
- **`databases.showAll` is gone**, replaced by `include` and `exclude` — glob
  lists, inherited like everything else. **The default flipped from closed to
  open**: a cluster naming no `include` reaches every database in it, where 0.1
  reached only `default`. A config that relied on the old default widens
  silently rather than failing, because it is a valid 0.2 config.
- **`databases.default` is only the entry point for catalogue queries**,
  defaulting to `postgres`. It no longer carries reachability of its own —
  `include` and `exclude` decide it like any other name, so
  `include: ["orders"]` reaches one database and not two.

### Security

- **The deny-list closes a class of functions, not the names it happened to
  know.** Entries match as globs, which closed three holes an exact list left
  open: `pg_file_write` from `adminpack`, `dblink_connect*`, and `pg_ls_*dir`.
  Of these `pg_logical_emit_message` is the one that needs no superuser.
- **The deny-list can no longer be removed by how the statement is written.**
  The scan lexes the statement the way postgres does, so a name is found inside
  a Unicode escape (`U&"..."`), an escape string (`E'...'`), or a pair of
  adjacent literals — and a comment marker inside a literal no longer blinds the
  rest of the scan. Before, `SELECT '--', pg_read_file('/etc/passwd')` disabled
  the list entirely.
- **`COPY … TO PROGRAM` is refused when the literal touches the keyword.**
  `COPY t TO PROGRAM'sh -c evil'` is valid postgres and used to pass the second
  lock, which split the statement on whitespace alone.

### Added

- **The instructions name every address.** The server lists its environments and
  clusters at `initialize`, marking the read-only ones: with three required
  arguments and no defaults, a model that does not know the names cannot call
  anything at all.
- **`readOnly` on an environment or a cluster** refuses every write tool at that
  address. It is inherited, defaults to false, and replaces the isolation a
  separate process per environment used to give.
- **`pg_read_status` prints the address table** — environment, cluster,
  read-only — without hosts and without pool state.

### Fixed

- **A named config path is never swapped for the XDG one.** A `--config` or
  `INFRA_MCP_POSTGRES_CONFIG` path that does not exist is reported as missing;
  it used to fall through to the XDG file, which after a typo in the variable
  seated the server on another environment's database.
- **`describe_table` refuses more than 20 tables in one call**, because it pays
  for each in round-trips rather than in output.

## [0.1.0] - 2026-08-23

Written retrospectively: the file starts here.

- First release. The postgres server end to end — six read tools and one write
  tool that asks first — installable as a Claude Code plugin from the
  marketplace in this repository.

[0.2.0]: https://github.com/Conte777/infra-mcp/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Conte777/infra-mcp/releases/tag/v0.1.0

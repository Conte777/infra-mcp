# infra-mcp

MCP servers giving Claude Code access to infrastructure — postgres, k8s, grafana,
redis, kafka, clickhouse — plus the marketplace that installs them, one plugin
per source.

Status: postgres works end to end and installs as a plugin. The five remaining
sources are next, one plugin each.

## Install

Requires Go (version in `go.mod`) on the machine: the plugin carries no binary.
It builds the pinned version on first start — about six seconds — into
`~/.cache/infra-mcp/`, then runs it directly, and every later start is
instant. Set `INFRA_MCP_GO` if `go` is not on the PATH Claude Code sees.

The postgres server talks to postgres 14 or newer — the oldest release postgres
itself still supports.

```sh
claude plugin marketplace add Conte777/infra-mcp
claude plugin install infra-mcp-postgres@infra-mcp
```

The server needs a config, and reports its absence through every tool call
until it has one. Inside a Claude Code session the launcher is on the PATH of
the Bash tool, so writing one is:

```sh
infra-mcp-postgres --init                  # writes the file and prints its path
infra-mcp-postgres --print-config-schema   # every key this build accepts
```

That lands in `$XDG_CONFIG_HOME/infra-mcp/postgres.json` (`~/.config/...` by
default); `INFRA_MCP_POSTGRES_CONFIG` points elsewhere. One file holds every
environment and every cluster, and one server reaches all of them — a tool call
names the environment, the cluster and the database:

```json
{
  "connection": { "user": "app", "password": "${PGPASSWORD}" },
  "environments": {
    "dev": {
      "connection": { "host": "dev.example.com" },
      "clusters": { "main": { "databases": { "default": "app_db" } } }
    },
    "prod": {
      "readOnly": true,
      "clusters": {
        "main": {
          "connection": { "host": "prod.example.com" },
          "databases": { "default": "app_db", "exclude": ["*_tmp"] }
        }
      }
    }
  }
}
```

Settings written above a cluster are inherited by every cluster below, and a
cluster overrides what it names — the user and password above are shared, the
host is per environment. `"readOnly": true` on an environment or a cluster
refuses every write tool at that address. Give the password as `${VAR}` — a
literal password in the file is a validation error. The config is read once at
startup, so restart the session after editing it.

### Approving the read tools once

The six read tools are marked read-only, but Claude Code still asks before each
call. One line in `~/.claude/settings.json` — or in a project
`.claude/settings.json` — covers all of them:

```json
{ "permissions": { "allow": ["mcp__plugin_infra-mcp-postgres_postgres__pg_read_*"] } }
```

Leave the write tool out. With no rule of its own it asks every time, which is
the point of it; and an `ask` rule broad enough to cover the server would win
over the `allow` above and take the read tools down with it.

### Updating

`claude plugin update infra-mcp-postgres` brings the binary with it: the
version in the plugin manifest is the tag the launcher builds.

## Development

Requires Go (version in `go.mod`) and [Task](https://taskfile.dev). Everything
else is installed into `./bin` at pinned versions by the Taskfile.

```sh
task hooks   # install the pre-commit hook (once per clone)
task         # fmt + lint + unit tests
```

| Task                   | What it does                                          |
| ---------------------- | ----------------------------------------------------- |
| `task fmt`             | rewrite sources with gofumpt                          |
| `task lint`            | format check, `go vet`, golangci-lint                 |
| `task test`            | unit tests with the race detector                     |
| `task test:integration`| integration tests against real containers (needs Docker) |
| `task vuln`            | govulncheck over the dependency tree                  |
| `task schema`          | regenerate the committed config JSON Schemas          |
| `task schema:check`    | fail if a committed schema drifted from its Go type   |
| `task tidy:check`      | fail if `go.mod`/`go.sum` drifted from the imports    |
| `task build`           | build every server binary into `./bin`                |

CI runs the same targets, so a green local run means a green pipeline.

Releasing a source: bump the version in every plugin manifest — `task
version:set -- 0.2.0` does it — head `CHANGELOG.md` with a section for that
version, and merge both to `main`. `task test` fails while the newest changelog
section and the manifests disagree, so neither half can go in without the
other. CI tags the commit `v<version>` once its matrix is green, and the launcher builds the tag it reads
from the manifest. A pushed tag is permanent: the module proxy caches the
revision it first saw, so a mistake is fixed by the next version, never by
moving the tag.

## Layout

```
cmd/infra-mcp-<source>/   one binary per source; the binary name is the release name
internal/                 implementation, not importable from outside the module
plugins/<source>/         one plugin per source: manifest, .mcp.json, launcher
.claude-plugin/           the marketplace listing those plugins
schema/                   JSON Schema per source config, generated from the Go types
docs/adr/                 architecture decision records
docs/agents/              conventions for the agents working in this repo
docs/research/            primary-source findings the architecture rests on
CONTEXT.md                domain glossary
```

## License

MIT — see [LICENSE](LICENSE).

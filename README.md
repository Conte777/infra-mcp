# infra-mcp

MCP servers giving Claude Code access to infrastructure — postgres, k8s, grafana,
redis, kafka, clickhouse — plus the marketplace that installs them as one plugin.

Status: the shared core is in place — config with a generated JSON Schema,
markdown rendering, the tool registry, both transports and the process
lifecycle. `infra-mcp-postgres` builds and serves MCP over stdio or streamable
HTTP, but so far answers only its status tool: the postgres tools themselves,
and the plugin that installs the servers, are still to come.

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

## Layout

```
cmd/infra-mcp-<source>/   one binary per source; the binary name is the release name
internal/                 implementation, not importable from outside the module
schema/                   JSON Schema per source config, generated from the Go types
docs/adr/                 architecture decision records
docs/agents/              conventions for the agents working in this repo
docs/research/            primary-source findings the architecture rests on
CONTEXT.md                domain glossary
```

## License

MIT — see [LICENSE](LICENSE).

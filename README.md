# infra-mcp

MCP servers giving Claude Code access to infrastructure — postgres, k8s, grafana,
redis, kafka, clickhouse — plus the marketplace that installs them as one plugin.

Status: early. Only the repository skeleton and tooling exist; no server is
implemented yet.

## Development

Requires Go (version in `go.mod`), [Task](https://taskfile.dev), and a Docker
daemon for the integration tests. Everything else is installed into `./bin` at
pinned versions by the Taskfile.

```sh
task hooks   # install the pre-commit hook (once per clone)
task         # fmt + lint + unit tests
```

| Task                   | What it does                                          |
| ---------------------- | ----------------------------------------------------- |
| `task fmt`             | rewrite sources with gofumpt                          |
| `task lint`            | format check, `go vet`, golangci-lint                 |
| `task test`            | unit tests with the race detector                     |
| `task test:integration`| integration tests against real containers             |
| `task vuln`            | govulncheck over the dependency tree                  |
| `task tidy:check`      | fail if `go.mod`/`go.sum` drifted from the imports    |
| `task build`           | build every server binary into `./bin`                |

CI runs the same targets, so a green local run means a green pipeline.

## Layout

```
cmd/infra-mcp-<source>/   one binary per source; the binary name is the release name
internal/                 implementation, not importable from outside the module
docs/adr/                 architecture decision records
CONTEXT.md                domain glossary
```

## License

MIT — see [LICENSE](LICENSE).

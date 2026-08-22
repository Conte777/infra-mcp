# infra-mcp

## What the environment doesn't say

**A source's config Go type and its committed schema move together.** `schema/<source>.schema.json` is generated from the Go type, so changing the type means running `task schema` — otherwise the `schema:check` gate goes red in CI.

**Done means every gate green locally.** The pre-commit hook runs formatting, linting and unit tests only, so a clean commit says nothing about the pipeline: govulncheck and the integration tests exist in CI alone. Run each target of the CI matrix (`.github/workflows/ci.yml`) before calling work finished — the integration ones need a running docker daemon.

**Language splits by audience.** Issues and the domain docs are Russian; code, comments, README, commit messages and branch names are English.

## Where the rest lives

- **Working the issue tracker** — create, read, label or close an issue; wayfinder maps and their child tickets: `docs/agents/issue-tracker.md`.
- **Triaging** — the label string behind each triage role: `docs/agents/triage-labels.md`.
- **Before exploring the codebase**, naming a domain concept, or recording a decision that grilling or a prototype settled: `docs/agents/domain.md`.
- **Claude Code plugins, the MCP Go SDK, MCP permissions** — the primary-source research the architecture rests on, each dated and pinned to the version it was checked against: `docs/research/`. `go-sdk.md` trails the version in `go.mod`, so re-check any signature it quotes.

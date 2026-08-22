# Code-Change Workflow

Binding for any `/wayfinder` ticket that changes code, and for any other agent session that does. Code never lands on `main` as a direct commit: it arrives as a reviewed, squash-merged pull request.

Code means anything under `cmd/`, `internal/`, `plugins/`, `schema/`, or the build and CI files that gate them. A change touching only prose — docs, ADRs, issue bodies — is out of scope here and commits directly; it rejoins this workflow the moment it ships alongside code.

## 1. Work in a worktree

Check out a fresh worktree before the first edit — never edit code in the primary clone. The frontier is designed to be worked by concurrent sessions, and they would fight over one working tree.

```
git worktree add .claude/worktrees/<slug> -b <type>/<slug>
```

`<slug>` is the ticket in three or four words (`describe-table-partitions`); `<type>` is the Conventional Commits type the work will carry (`feat`, `fix`, `docs`). `.claude/worktrees/` is gitignored. `EnterWorktree` places worktrees here too, so either door is fine.

`GOBIN` resolves per worktree, so the first `task` run in a new one reinstalls the pinned tools into its own `./bin` — a slow first invocation, not a broken one.

## 2. Green every gate before the PR

Run each target of the CI matrix in `.github/workflows/ci.yml`, not just what the pre-commit hook covers: `task lint`, `task test`, `task schema:check`, `task tidy:check`, `task vuln`, `task test:integration` (needs a docker daemon).

## 3. Open the PR

```
gh pr create --title "<type>: <what changed>" --body "..."
```

Reference the ticket as `Refs #<n>` — **not** `Closes #<n>`. The ticket is closed by wayfinder's resolution step, which posts the answer comment first; a GitHub auto-close on merge would skip that comment and leave the map's Decisions-so-far pointing at a ticket that never recorded its answer.

## 4. Run both reviews, then fix once

Launch both in the same turn, in report mode:

- `/code-review <PR#>` — correctness bugs, reuse and simplification, against the PR diff. Pass no `--fix` here.
- `/mattpocock-skills:code-review main` — the Standards and Spec axes; Spec reads the ticket the PR references.

Wait for both reports before touching the tree. Applying fixes while the other review is still reading the diff makes its findings stale — that is why the built-in review runs without `--fix` in this flow.

Then fix in one pass: commit on the same branch, push, re-run the gates from step 2. A finding you disagree with gets a PR comment saying why, not silence.

## 5. Merge and clean up

Merge once CI is green:

```
gh pr merge <n> --squash --delete-branch
git worktree remove .claude/worktrees/<slug>
```

Squash is what this repo's history uses — `feat: add postgres plugin to marketplace (#34)`.

Only then resolve the wayfinder ticket per the Resolve step in `issue-tracker.md`.

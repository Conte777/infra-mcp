# Domain Docs

## Before exploring, read these

- **`CONTEXT.md`** at the root, in full — the glossary is short, and most terms are anchored to the package, type or registration door that implements them.
- The ADRs in **`docs/adr/`** that touch the area you're about to work in — each holds a decision and the reasoning behind it.

## Use the glossary's vocabulary

When your output names a domain concept — an issue title, a refactor proposal, a hypothesis, a test name — use the term as `CONTEXT.md` defines it; each entry's _Avoid_ line names the synonyms that term displaces.

A concept the glossary doesn't have is a signal: either you're inventing language the project doesn't use (reconsider), or there's a real gap (note it for `/domain-modeling`).

## Record a decision before its issue closes

A decision reached by grilling or by a prototype becomes an ADR in `docs/adr/` before the issue closes: a resolution comment is unreachable from the code, so the next agent re-derives what was already argued out. Take the format and the three conditions that make a decision ADR-worthy from `/domain-modeling` — whether this one qualifies is answered there.

Every ADR here also carries a **Состояние в коде** section: where the decision lives in `internal/`, and each place the code has since diverged from it. Touching the code an ADR describes means bringing that section back in line.

## Flag ADR conflicts

When your output contradicts an existing ADR, say so in the output itself:

> _Contradicts ADR-0003 (ядро владеет процессом) — but worth reopening because…_

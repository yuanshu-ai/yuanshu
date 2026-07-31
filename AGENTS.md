# Yuanshu Repository Instructions

## Source of truth

Before implementation work, read these local documents when they exist:

1. `docs/ai-coding-status.md`
2. the active Task card in `docs/ai-coding-execution-plan.md`
3. only the product, architecture, or MVP sections referenced by that Task

User instructions override the documents. Product requirements and accepted architecture decisions override the execution plan. If they conflict, report the conflict instead of silently choosing.

## Task discipline

- Work on one active `AC-xxx` Task at a time unless the user explicitly creates a bounded multi-Task goal.
- Respect the Task's dependencies, allowed scope, non-goals, verification, acceptance, and stop conditions.
- Do not mark a Task complete until its acceptance checks pass and its implementation is committed.
- Update `docs/ai-coding-status.md` when a Task starts, verifies, completes, or blocks; commit status changes when the active goal, Batch, or Gate ends.
- Use Plan mode before changing protocol, trust boundaries, persistence schemas, public APIs, or accepted ADRs.
- Goal mode should cover only 1–4 closely related Tasks, never the entire M0–M5 roadmap.

## Architecture constraints

- MVP uses one root Go module and one `yuanshu` binary with `server`, `node`, and `standalone` subcommands.
- Server must never bypass the Node boundary to control an Agent Runtime.
- Never expose Codex app-server, Claude Code internals, debug ports, or credentials directly to the public internet.
- Keep protocol, transport, adapters, configuration, and event logic platform-neutral.
- Isolate Windows, macOS, and Linux behavior behind the Platform abstraction and build tags.
- Prefer pure-Go dependencies. Adding CGO requires an ADR and cross-platform build/supply-chain review.
- Do not introduce microservices, PostgreSQL, Redis, message queues, Electron, or cloud-only dependencies without an explicit approved decision.

## Verification and Git

- Preserve unrelated user changes in dirty worktrees.
- Run the Task's verification commands plus `git diff --check`.
- Add or update tests for behavior changes.
- Commit each completed substantial change with a focused Conventional Commit message.
- The root repository and `docs/` are separate Git repositories; commit them separately when both change.
- Do not push unless the user explicitly asks.
- Never commit credentials, tokens, cookies, private task content, or unredacted local paths.

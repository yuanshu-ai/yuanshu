# Contributing to Yuanshu

Yuanshu welcomes focused fixes, compatibility findings, security review, documentation improvements, and proposals that strengthen the personal remote-agent workflow.

The project is pre-alpha. Please open an Issue before investing in a large feature, new Agent adapter, persistence change, or trust-boundary redesign.

## Development setup

Install Go, Node.js, and pnpm versions capable of building the repository. `.go-version` and `.node-version` record the currently verified toolchains; they are recommendations, not product runtime allowlists.

```shell
corepack enable
pnpm install --frozen-lockfile
pnpm build
go build ./...
```

The repository uses one Go module and one `yuanshu` binary with `server`, `node`, and `standalone` subcommands. Web workbench source is under `web/`; the local Node control center is under `node-web/`.

## Before changing code

- Preserve the Node as the final security boundary for Agent Runtime access, workspaces, permissions, and approvals.
- Never expose Codex app-server, debug ports, credentials, or arbitrary local paths to the public internet.
- Keep Protocol, Transport, adapters, configuration, and event logic platform-neutral.
- Isolate OS behavior behind the Platform abstraction and build tags.
- Do not introduce a new service, database, queue, Agent adapter, CGO dependency, or public wire type without an accepted design decision.
- Treat generated Protocol and status files as outputs of their source Schema/catalog, not hand-edited copies.

Changes to Protocol, persistence schemas, trust boundaries, public APIs, or accepted ADRs require an explicit plan and corresponding compatibility, migration, and security tests.

## Tests

Run checks appropriate to the change and the full gate before requesting merge:

```shell
pnpm readme:check
pnpm protocol:test
pnpm protocol:check
pnpm status:check
pnpm --dir web test --run
pnpm --dir web test:e2e
pnpm --dir web build
pnpm --dir node-web test --run
pnpm --dir node-web build
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

Web builds intentionally update the embedded deterministic assets under `internal/server/webassets/dist` and `internal/node/webassets/dist`. Commit those generated changes with the source change.

## Pull requests

- Keep the change focused and preserve unrelated work.
- Add or update tests for behavior changes.
- Explain security and data-boundary effects, even when the answer is “none.”
- Include screenshots for visible UI changes.
- Use Conventional Commit subjects such as `feat(node):`, `fix(web):`, `docs:`, or `test(server):`.
- Do not commit credentials, tokens, cookies, private task content, unredacted local paths, or private Relay addresses.

Public user guidance lives in [`guides/`](./guides/README.md). Deeper design, execution, and acceptance records are maintained in a separate documentation repository; when a maintainer asks for changes in both repositories, keep their commits and pull requests separate.

## Reporting problems

Use the provided GitHub Issue forms for ordinary bugs and feature proposals. Do not publish security vulnerabilities in an Issue; follow [SECURITY.md](./SECURITY.md) instead.

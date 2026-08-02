# Yuanshu · 远枢

> Open-source remote workspace for local AI coding agents.

[English](./README.md) | [简体中文](./README.zh-CN.md)

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)
[![Status](https://img.shields.io/badge/status-pre--alpha-orange.svg)](#project-status)
[![CI](https://github.com/yuanshu-ai/yuanshu/actions/workflows/ci.yml/badge.svg)](https://github.com/yuanshu-ai/yuanshu/actions/workflows/ci.yml)

Yuanshu is being built for developers who want to control AI coding agents running on their own computers from a phone, tablet, or browser. It focuses on structured agent activity—threads, streaming output, commands, diffs, approvals, and task state—instead of forwarding an entire desktop.

The name **远枢** combines “remote” (远) and “hub/pivot” (枢): a remote control hub for the machines and agents you already use.

> [!IMPORTANT]
> Yuanshu is currently in pre-alpha design and technical validation. There is no installable release yet. Do not use the project to expose a development machine to the public internet.

## Why Yuanshu?

- **One controller, many nodes** — Follow work across a personal PC, work machine, always-on computer, or development server from one interface.
- **Authentication-neutral** — Reuse the Codex setup already working on each node machine, whether it uses ChatGPT sign-in, an API key, or a supported custom provider.
- **Local execution and credentials** — Model requests, source code, shell access, Git/SSH credentials, and agent authentication stay on the node machine.
- **Agent-native remote experience** — Display task state, streamed events, approvals, commands, and diffs instead of mirroring a desktop.
- **Open and self-hostable** — Yuanshu Node, Server, Web, protocol, and adapters are intended to remain auditable and self-hostable.
- **Designed to grow beyond one agent** — The first adapter targets Codex app-server; additional local coding agents can be added through explicit capability adapters.

## Planned personal MVP

The first usable version is intentionally focused:

- One owner controlling 1–5 Yuanshu Nodes;
- Windows 11 x64 Node first;
- Linux amd64 Server and Standalone as the initial self-hosting target;
- Codex app-server integration;
- Local workspace allowlist;
- Create, list, read, and resume threads;
- Stream agent messages, commands, tool activity, file changes, and diffs;
- Steer or interrupt an active turn;
- Review and resolve approvals from a mobile-friendly PWA;
- Signed control messages verified by the Node;
- Node-side event journal, reconnect, replay, and snapshot recovery;
- Standard Server + Node and single-deployment Standalone modes;
- Outbound-only HTTPS/WSS connections for ordinary Node machines.

Team roles, hosted compute, remote desktop, a general-purpose web terminal, and permanent cloud storage of task content are deliberately outside the first MVP.

## Platform roadmap

Windows, macOS, and Linux are all first-class product targets. The order is phased to keep the first release achievable:

1. Windows 11 x64 Yuanshu Node;
2. Linux amd64 Yuanshu Server and Standalone;
3. Linux amd64 Yuanshu Node;
4. macOS arm64 Yuanshu Node;
5. macOS amd64 and Linux arm64 builds based on actual usage.

The protocol, transports, adapters, configuration model, and event journal will share one Go implementation. Platform-specific code is limited to secure storage, IPC, process lifecycle, autostart, path validation, and release signing. The project prefers pure-Go dependencies; introducing CGO requires an explicit cross-platform build and supply-chain review.

The shared Platform contract is established for all three target families. Windows now provides current-user DPAPI secure storage, handle-based workspace inspection, and direct user-process lifecycle management; other unimplemented production capabilities continue to fail closed. Stateful in-memory fakes cover secure storage, direct process lifecycle, logical local IPC, current-user autostart, and workspace facts. Keychain, Secret Service, Named Pipe, Unix socket, Job Object, LaunchAgent, and systemd integrations remain later platform tasks. Workspace inspection reports operating-system facts only—the Node policy layer makes every allow/deny decision.

## Architecture direction

```mermaid
flowchart TB
    Client["Phone / Tablet / Browser"]
    Server["Yuanshu Server<br/>Web + Pairing + Routing + Relay"]

    subgraph Machine["Your computer or server"]
        Node["Yuanshu Node<br/>Local bridge & security boundary"]
        Adapter["CodexAdapter"]
        Codex["Codex app-server"]
        Workspace["Allowed Workspaces"]
        Credentials["Local Auth & Provider Credentials"]
    end

    Client -->|"HTTPS / WSS"| Server
    Node -->|"Outbound WSS / RelayTransport"| Server
    Server -->|"Signed control messages"| Node
    Node --> Adapter
    Adapter --> Codex
    Codex --> Workspace
    Codex --> Credentials
```

Yuanshu has four logical components: the Agent Runtime (Codex first, Claude Code later), Yuanshu Node, Yuanshu Server, and the browser/PWA Control Client. The Server provides Web, pairing, registry, routing, and relay capabilities. The Node remains the final enforcement point for trusted controllers, workspace boundaries, sandbox policy, and approvals.

The planned runtime modes are:

```text
yuanshu server       Web, control plane, and relay
yuanshu node         Local bridge connecting an Agent Runtime to a Server
yuanshu standalone   Server + Web + local Node in one deployment
```

`yuanshu node` is the formal Windows user-session Alpha entry. It loads the versioned local configuration, owns Codex child processes, exposes a current-user-only management pipe, and provides a native tray icon. `yuanshu server` now starts the formal minimal metadata and bootstrap service on an explicit loopback address. `standalone` remains the disposable M0 engineering PoC. Codex app-server and other agent internals must never be exposed directly to the public internet.

## Project status

- [x] Product scope and architecture baseline
- [x] Personal-first, one-to-many-Node direction
- [x] Authentication-neutral Codex positioning
- [x] Buildable workspace, placeholder CLI, Web scaffold, and base CI
- [x] M0 Codex app-server and minimal vertical proof of concept
- [x] Protocol v1 Schema, generated Go/TypeScript types, and compatibility fixtures
- [x] JCS + Ed25519 control encoding and cross-language test vectors
- [x] Node-side signed control validation and atomic replay protection
- [x] Transport contract and shared Relay/Standalone behavior tests
- [x] Windows/macOS/Linux Platform contract, safe skeletons, and stateful fakes
- [x] Windows DPAPI identity storage and local workspace policy boundary
- [x] Node-managed Codex stdio Runtime, formal Adapter contract, and Thread/Turn ownership
- [x] Bounded Node event journal, cursor replay, snapshots, and ambiguous recovery
- [x] Windows Yuanshu Node alpha
- [x] Native three-platform CI, containerized Linux race, dependency/secret scanning, and SBOM
- [x] Formal loopback Server bootstrap and SQLite metadata baseline
- [x] TLS-only WSS Hub, authenticated RelayTransport, and Owner/Node routing
- [ ] Linux Server and Standalone self-hosting preview
- [ ] Self-hosted device and control-client pairing
- [ ] Linux Yuanshu Node
- [ ] macOS arm64 Yuanshu Node
- [ ] Mobile PWA task loop
- [ ] Security hardening and first public preview

The roadmap establishes a reliable daily-use loop for one developer first, then completes the committed Linux and macOS integrations before expanding into more agent adapters or team features.

## Development

The repository contains both the isolated `m0-poc-1` Gate G0 implementation and the formal internal CodexAdapter foundation. The formal Adapter uses a Node-managed stdio app-server, local workspace IDs, bounded events, one-shot approvals, and persisted Thread ownership. Node SQLite now also provides monotonic event sequences, bounded retention, outbox cursor acknowledgement, replay, snapshots, and conservative reconciliation of uncertain Turns. The formal TLS-only WSS Hub and RelayTransport are implemented; ordinary pairing, Node Runner wiring, and the PWA are not connected yet.

Prerequisites:

- Go 1.26.5;
- Node.js 24.18.1;
- pnpm 11.18.0 through Corepack.

Install the Web dependencies from the repository root:

```shell
corepack enable
corepack install --global pnpm@11.18.0
pnpm install --frozen-lockfile
```

Run the local verification suite:

```shell
go test ./...
go test ./internal/platform/... ./tests/contract/platform/...
go test ./internal/config/... ./tests/contract/config/...
go test ./internal/node/... ./tests/contract/node/...
go test ./internal/adapter/... ./tests/integration/codex/...
go vet ./...
go build ./...
pnpm --dir web test
pnpm --dir web build
```

The formal Protocol v1 Schema is the sole wire-type source. Regenerate and verify the committed Go/TypeScript types with:

```shell
pnpm protocol:generate
pnpm protocol:check
pnpm protocol:test
```

Protocol generation requires both Node.js and Go (`gofmt`) and is deterministic on Windows, macOS, and Linux. The temporary `m0-poc-1` frames are intentionally separate from Protocol v1.

### Formal Node configuration

The versioned Node configuration contract uses strict TOML and is defined by `schemas/config/v1/node-config.schema.json`. It currently accepts `relay` and `standalone` transport modes and Codex `stdio` only. Device, Relay, and proxy credentials are represented solely by opaque SecretRef values; configuration files never contain credential bytes, and an unavailable secure store never triggers a plaintext fallback.

The configuration package supports atomic replacement, a last-known-good `.bak` file, explicit recovery status, and sanitized SecretRef health checks. On Windows, configured workspaces are reconciled into the Node's local SQLite policy store. Remote callers use only opaque workspace IDs; canonical paths, stable file identities, reparse-point checks, and read/write/network ceilings remain local. The formal CodexAdapter consumes that boundary, while mapped Protocol v1 events, cursor replay, snapshots, and conservative ambiguous recovery survive Node restarts.

The Windows Alpha uses `%LOCALAPPDATA%\Yuanshu\config.toml` by default. It runs in the current user session, shows a native tray menu, uses a current-user-only Named Pipe, protects Agent process trees with a Job Object, and can be explicitly enabled at login through HKCU. The tray opens or reloads configuration, copies a sanitized diagnostic report, toggles autostart, and exits the Node; it is deliberately not a second settings UI. Real Relay connection and device pairing are not yet available in this build.

```powershell
yuanshu node
yuanshu node status --json
yuanshu node doctor
yuanshu node autostart enable
yuanshu node autostart disable
yuanshu node stop
```

Inspect the CLI without starting any service:

```shell
go run ./cmd/yuanshu --help
go run ./cmd/yuanshu server --help
go run ./cmd/yuanshu node --help
go run ./cmd/yuanshu standalone --help
```

### Formal Server bootstrap

The minimal Server requires an explicit absolute data directory and listens only on the literal loopback addresses `127.0.0.1` or `::1`:

```powershell
yuanshu server --data-dir C:\path\to\yuanshu-server --listen 127.0.0.1:7444
```

On an uninitialized data directory, the Server prints a 32-byte bootstrap secret once to local stdout. The enrolling Node generates its own Ed25519 key and connection credential, retains the credential locally, and sends only the public key and SHA-256 credential hash to `POST /v1/bootstrap/claim`. The Server persists `server.db`, creates the first Owner and Node atomically, and supports exact claim retries for five minutes. HTTP initialization uses `/healthz`, `/readyz`, `/v1/bootstrap/status`, and `/v1/bootstrap/claim`; authenticated realtime connections use `/node/connect` and `/web/connect`.

The formal realtime handlers require TLS, authenticate Node credentials plus Ed25519 challenges, and route immutable Protocol v1 frames without re-encoding them. The current CLI still starts loopback HTTP, so its WebSocket endpoints return `tls_required`; certificate flags and public deployment remain AC-306. Ordinary pairing and the Web UI are also not available yet. Do not expose the loopback listener through a reverse proxy or outside the local machine.

The repository uses LF source files and supports these commands on Windows, macOS, and Linux. CI runs native Go checks on Ubuntu 24.04 x64, Windows Server 2025 x64, and macOS 15 arm64; Web/Protocol checks, a pinned-container Linux race suite, dependency and Secret scanning, and an SPDX SBOM are release gates. Successful runs retain unsigned Windows amd64, Linux amd64, and Darwin arm64 build artifacts for seven days. These are engineering artifacts, not installable releases; signed releases and product container images remain later milestones.

### M0 PoC (developers only)

The isolated internal PoC harness and the `standalone` PoC use explicit temporary configuration:

```text
YUANSHU_POC_LISTEN=127.0.0.1:7443
YUANSHU_POC_TLS_CERT=<localhost certificate PEM>
YUANSHU_POC_TLS_KEY=<localhost private key PEM>
YUANSHU_POC_NODE_TOKEN=<at least 32 random bytes>
YUANSHU_POC_SERVER_URL=wss://localhost:7443
YUANSHU_POC_WORKSPACE=<existing non-root disposable directory>
```

`YUANSHU_POC_ARCHIVE_ON_CLOSE=1` is reserved for bounded test runs. These settings do not configure the formal `yuanshu server` or `yuanshu node` commands. Do not reuse the PoC token or development certificate, and do not expose the PoC outside loopback.

## Security principles

Yuanshu is designed around a high-trust local execution environment and a minimally trusted Server relay:

- Node and control clients use independent device identities;
- Control operations and approval decisions are signed end to end and revalidated by the Node;
- Remote clients cannot choose arbitrary local paths;
- The Server relay does not permanently store prompts, responses, command output, diffs, or source code;
- ChatGPT tokens, API keys, provider keys, and Git/SSH credentials must never be uploaded to Yuanshu;
- Ambiguous operations after a crash must not be silently replayed.

These are design goals until the corresponding implementation and security tests land. A formal security policy and private reporting channel will be published before the first executable release.

## Contributing

Yuanshu is at an early stage, so focused feedback is especially useful. Good first contributions include:

- Codex app-server compatibility findings;
- Windows, Linux, and macOS Node lifecycle experiments;
- Protocol and threat-model review;
- Mobile task and approval UX proposals;
- Self-hosting feedback;
- Cross-platform integration findings or research on the next agent adapter.

Please use GitHub Issues for reproducible bugs, scoped proposals, and design discussion. Avoid posting credentials, private source code, or security vulnerabilities in public issues.

Contribution guidelines and a security reporting process will be added as the implementation begins.

## License

Yuanshu is licensed under the [Apache License 2.0](./LICENSE).

## Acknowledgements

The initial integration is designed around the open-source [OpenAI Codex](https://github.com/openai/codex) app-server protocol.

Yuanshu is an independent open-source project and is not affiliated with or endorsed by OpenAI. Product and company names are trademarks of their respective owners.

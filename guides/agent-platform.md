# Agent platform direction

Yuanshu is Codex-first, not Codex-only. Codex app-server remains the only production
Adapter in the current pre-alpha. The platform work completed so far makes the Node
capable of describing and isolating more than one local Agent without exposing Agent
credentials or private protocols to Yuanshu Server.

This boundary is especially useful when Codex uses API-key authentication, a custom
Base URL, an enterprise model gateway, a proxy, MCP servers, or other local tools.
Model requests and credentials remain in the Agent's local environment; Yuanshu Server
and the browser do not become a model proxy or credential store.

## Implemented foundations

- A compile-time Adapter Registry creates the built-in `codex-default` instance without
  coupling the Node Host, Standalone, or doctor command to Codex construction.
- Generic Agent, Instance, Runtime, installation, compatibility, and capability
  descriptors sit above Agent-private protocols.
- A local in-memory Inventory detects bounded installation and process state without
  returning executable paths, PIDs, command lines, environment variables, or
  credentials.
- A Runtime Manager keys managed runtimes by Instance and Endpoint, with isolated event
  pumps, health, backpressure, and reverse-order process cleanup.
- Codex managed stdio remains the only production runtime path. Protocol 1.0 remains
  compatible, while Node configuration and SQLite now have typed Agent resources and
  Task Bindings; existing Thread/Turn and recovery semantics remain compatible.

The persisted managed/detected-only resources are now projected through Protocol 1.1 and
the Web workbench. They do not make Claude Code or OpenCode remotely controllable: only
the Codex managed instance exposes task controls.

## Target resource model

- **Installation** is a locally detected Agent binary and its redacted compatibility
  state.
- **Agent instance** is one configured Agent environment on one Node, such as
  `codex-default`.
- **Runtime endpoint** is one managed, attached, history-only, or detected-only access
  path for an instance.
- **Yuanshu task** is the stable remote task resource used by leases, notifications, and
  recovery.
- **Native session** is the Agent's own Thread, Session, or Conversation identifier and
  must remain on the Node.
- **Task binding** is the Node-local mapping between a Yuanshu task, Agent instance,
  runtime endpoint, native session, and workspace.

The Registry, Inventory, managed Runtime Manager, Config v2, SQLite v8, and Task Binding
store exist now. Protocol 1.0 remains compatible while Protocol 1.1 carries the typed
Agent/Task/Run/Activity/Interaction envelope. Native Session IDs and attached locators
remain Node-local.

## Runtime modes and evidence

Runtime availability is capability-based, not inferred from an Agent name or a running
process:

- `managed` means the Node starts, owns, and safely closes the structured runtime;
- `attached` is a proposed mode for an explicitly authenticated runtime owned elsewhere;
- `history-only` can read native history but cannot claim live control;
- `detected-only` reports bounded installation or process state and creates no task
  controls.

The production Codex path is `managed`. A bounded Codex attachment experiment proved
that an explicitly supplied loopback WebSocket can require a capability token, but it
did not prove reliable cross-process native-session discovery and history reading.
External Codex CLI/Desktop attachment therefore remains unavailable.

Evidence-only probes also examined Claude Code `2.1.212` stream JSON and OpenCode
`1.18.13` authenticated HTTP/OpenAPI/SSE surfaces. OpenCode currently has the stronger
structured candidate surface, but neither probe is registered as a production Adapter,
and the observed versions are evidence records rather than runtime allowlists.

## Capability-driven workbench

A future multi-Agent workbench will expose history, execution, steer, queue, interrupt,
approvals, command/tool activity, Diff review, branches, worktrees, attachments, or
subagents only when the selected runtime reports that capability. Missing or degraded
capabilities must remain explicit; the UI must never fabricate Codex-equivalent behavior.

The Server stays Agent-agnostic. It authenticates devices, routes signed controls,
maintains task leases, and stores only redacted operational metadata. It never loads an
Adapter, proxies a model request, or persists Agent credentials, native session IDs, or
complete task content.

## Current gates

1. Finish PF-052 real-device acceptance for the personal Codex workbench and the new
   Agent/Task resource path.
2. Keep external attached/history-only discovery blocked after the negative PF-084 gate;
   managed/detected-only persistence is already implemented and must be tested on the
   supported platforms.
3. Require a bounded real-model gate before promoting OpenCode or Claude Code beyond an
   evidence-only probe.
4. Treat Protocol 1.1 as an implemented, verifying compatibility layer and only expand
   its shared semantics when additional structured Agent evidence supports them.

Yuanshu will not load arbitrary remote plugins, scan arbitrary local ports, expose Agent
debug endpoints, or rely on terminal-color parsing as the only basis of a production
Adapter.

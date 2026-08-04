# Agent platform direction

Yuanshu is Codex-first, not Codex-only. The current pre-alpha has one production
Adapter—Codex app-server—but its long-term role is to provide secure remote access to
the coding-agent environment that already runs on the user's own machine.

This matters when Codex uses API-key authentication, a custom Base URL, an enterprise
model gateway, a proxy, MCP servers, or other local tools. Yuanshu Node launches Codex
in that local user environment. Model requests and credentials do not pass through
Yuanshu Server or the browser.

## Stable boundaries

- **Adapter type** identifies an implementation such as Codex, Claude Code, or OpenCode.
- **Agent instance** identifies one configured runtime on one Node.
- **Yuanshu task** is the stable remote task resource used by leases and recovery.
- **Native session** is the Agent's own Thread/Session identifier and remains on the Node.

The Server stays Agent-agnostic. It authenticates devices, routes signed controls,
maintains task leases, and stores only redacted operational metadata. It never loads an
Adapter, proxies a model request, or persists an Agent credential.

## Capability-driven UI

Future Adapters will report their actual runtime capabilities instead of pretending to
implement every Codex feature. The workbench may expose task history, steer, queue,
interrupt, approvals, command/tool activity, Diff review, branches, worktrees,
attachments, or subagents only when the selected Agent instance supports them.

Unsupported and degraded capabilities remain visible as explicit status, never as a
fabricated success.

## Delivery order

1. Finish PF-052 real-device acceptance for the personal Codex workbench.
2. Replace direct Codex construction with a static Adapter Registry.
3. Add local Agent instances and stable Yuanshu task-to-native-session bindings.
4. Add versioned runtime capability negotiation.
5. Complete Codex-native workbench parity on that capability model.
6. Run structured-interface spikes for Claude Code and OpenCode before selecting the
   second production Adapter.

Yuanshu will not load arbitrary remote plugins or rely on terminal-color parsing as the
only foundation for a production Adapter.

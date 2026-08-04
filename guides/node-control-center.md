# Node local control center

Windows exposes a native system tray entry and macOS exposes an AppKit menu bar entry. They remain lightweight and open the same on-demand local Web control center for detailed status and non-secret settings.

Open it from the tray/menu or run:

```shell
yuanshu node ui
```

The control center is not a permanent HTTP listener. It uses an ephemeral `127.0.0.1` port, a short-lived one-time URL-fragment token, a memory-only session, strict Host/Origin checks, CSP, and idle shutdown. It does not store or expose private keys, credentials, Prompts, control bodies, raw TOML, or absolute workspace paths.

Current pages cover:

- Node, Relay, Codex, identity, recovery, workspace, and autostart status;
- Relay address, proxy, timeout, and event-retention settings;
- Codex detection and compatibility guidance without a version allowlist;
- existing workspace names and current permission/network state;
- pairing entry points, known control clients/devices, pending configuration, and redacted diagnostics.

Sensitive setting changes display a structured summary: category, redacted before/after values, risk, Relay reconnect impact, permission direction, and expiry. Approval or rejection happens in the trusted local UI or local CLI. The tray/menu only signals that review is required; it does not approve by field name.

Linux Node desktop integration is not supported in the current pre-alpha.

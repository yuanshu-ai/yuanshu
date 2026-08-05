# Configuration reference

Yuanshu uses separate strict TOML files for Server and Node. Secrets remain in operating-system secure storage and are referenced by opaque IDs; configuration files never contain credential bytes.

## Server configuration

Server config v2 contains:

- `deployment_mode`;
- private `data_dir`;
- listener and browser-facing `public_url`;
- allowed HTTPS control origins;
- TLS termination or ACME settings where required;
- embedded Web and Admin enablement and bounded session/audit settings.

Create it with the local wizard rather than hand-writing TOML:

```shell
yuanshu server setup --config /absolute/path/server.toml
```

Server config v1 remains readable. Explicit setup or migration writes v2 only after creating a `.bak` and validating the complete replacement. Listener, TLS material, private keys, data directories, deployment mode, and Origin policy remain local CLI/setup responsibilities and cannot be changed through remote Admin.

## Node configuration

Node configuration includes:

- display identity and `relay` or `standalone` transport;
- Relay WSS URL, optional proxy, timeout, and optional local CA bundle;
- Codex stdio settings and local SecretRefs; API Key, custom Base URL authentication,
  Provider headers, and Agent credentials remain managed by Codex or the local OS and
  are not copied into Yuanshu configuration;
- event retention;
- registered workspace IDs, display names, canonical local paths, read/write policy, and network ceiling.

Create or repair it with:

```shell
yuanshu node setup
yuanshu node ui
yuanshu node doctor --json
```

The public Node schema is Config v2. It exposes typed Agent Instances with explicit
`managed` or `detected-only` modes, Codex stdio settings, and Workspace allowed/default
instance relationships. The current builtin production instance is `codex-default`; a
detected-only entry cannot create or control tasks. Config v1 is migrated in memory to v2
and future versions are rejected rather than silently downgraded.

Node SQLite v8 mirrors the validated resources and keeps the Native Session ID only on
the Node. Do not add arbitrary Agent-specific TOML fields, attached endpoints, sockets,
ports, credentials, or paths: those remain outside the supported schema.

When automatic browser opening or the native directory picker is unavailable, keep the setup local by printing the one-minute loopback URL and preselecting the workspace from the CLI:

```shell
yuanshu node setup \
  --print-url \
  --workspace /absolute/path/to/workspace
```

For a `lan-managed` Server, the same local command can preselect the public CA certificate without exposing its path or PEM contents to the setup page:

```shell
yuanshu node setup \
  --print-url \
  --workspace /absolute/path/to/workspace \
  --relay-ca /absolute/path/to/yuanshu-lan-ca.crt
```

The printed URL is loopback-only and expires after one minute. Workspace selections are represented in the browser by a session-bound, single-use opaque token; the Node rechecks the canonical path and local security boundary before saving it.

Remote Web settings return a redacted view only. Safe display and retention changes may apply directly. Relay, proxy, workspace, permission, or execution-boundary changes create a pending record that must be reviewed on the Node machine. Remote callers cannot add a workspace, submit an absolute path, replace a CA, change credentials, or expand permissions without the local boundary.

## Files and recovery

- Configuration directories are private to the current user.
- Writes use temporary files, fsync, atomic replacement, and a last-known-good backup.
- Invalid updates preserve the previous effective configuration.
- Secure Store failure is fail-closed and never falls back to plaintext.
- Node event and SQLite continuity is preserved when a Relay-only setting safely reloads.

The machine-readable contracts are in [`schemas/config/v2`](../schemas/config/v2); v1 is
retained only for strict migration fixtures and compatibility tests.

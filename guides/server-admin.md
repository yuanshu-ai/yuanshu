# Server administration console

Open `/admin` from the same HTTPS origin as the Server. The console is operational and security management for a personal Server; it is not a task workbench and does not initialize Thread state.

An active paired control-client identity signs a short-lived admin challenge with its non-exportable browser key. The resulting session uses HttpOnly, SameSite=Strict, Host-only cookies; remote HTTPS deployments also require Secure cookies. Mutations additionally require same-origin checks, CSRF, JSON content type, idempotency, and one-time action signatures for destructive operations.

## Available views

- Server uptime, build, database health, deployment mode, TLS Provider, SAN, certificate expiry, and backup summary;
- Nodes and control clients with redacted online, connection, Runtime, Relay, recovery, and error state;
- pending pairing/enrollment requests and admission switches;
- active Thread leases and epoch-safe administrative release;
- redacted configuration, diagnostics, and management audit records.

Admin may revoke a non-final Node or control client, cancel pending requests, release a matching lease, and close new admission. It cannot approve pairing/enrollment, control Codex, read task content, edit raw TOML, change deployment mode, trigger backup/restore, or upload/download TLS private keys.

Server database backup and restore remain local CLI operations:

```shell
yuanshu server backup --config /absolute/path/server.toml
yuanshu server restore --config /absolute/path/server.toml --from /absolute/path/backup.tar.gz
```

Restore requires the Server to be stopped and validates the fixed archive, checksums, schema, and SQLite quick check before atomic replacement.

The audit store contains operation type, redacted resource identifiers, result, error code, correlation ID, and time. It must not contain Prompts, Agent output, commands, Diffs, absolute paths, credentials, public keys, tokens, IP addresses, or complete User-Agent strings.

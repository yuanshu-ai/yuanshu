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
- Codex stdio settings and local SecretRefs;
- event retention;
- registered workspace IDs, display names, canonical local paths, read/write policy, and network ceiling.

Create or repair it with:

```shell
yuanshu node setup
yuanshu node ui
yuanshu node doctor --json
```

Remote Web settings return a redacted view only. Safe display and retention changes may apply directly. Relay, proxy, workspace, permission, or execution-boundary changes create a pending record that must be reviewed on the Node machine. Remote callers cannot add a workspace, submit an absolute path, replace a CA, change credentials, or expand permissions without the local boundary.

## Files and recovery

- Configuration directories are private to the current user.
- Writes use temporary files, fsync, atomic replacement, and a last-known-good backup.
- Invalid updates preserve the previous effective configuration.
- Secure Store failure is fail-closed and never falls back to plaintext.
- Node event and SQLite continuity is preserved when a Relay-only setting safely reloads.

The machine-readable contracts are in [`schemas/config/v1`](../schemas/config/v1).

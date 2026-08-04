# Support

Yuanshu is pre-alpha, source-build software. There is no production SLA, paid support channel, guaranteed migration path, or supported installer yet.

## Start here

1. Read the [README](./README.md) and choose the correct deployment mode.
2. Review the [self-hosting documentation](./guides/self-hosting.md).
3. Run the relevant redacted diagnostics:

```shell
yuanshu server doctor --config /absolute/path/server.toml --json
yuanshu node doctor --json
yuanshu node status --json
```

Before sharing output, remove private addresses, absolute paths, identity information, task content, command output, Diffs, credentials, keys, cookies, and tokens.

## Where to ask

- Reproducible product or build bug: use the GitHub bug report form.
- Scoped feature proposal: use the feature request form and describe the personal-use problem first.
- Codex compatibility finding: include the detected version and capability result, but no authentication data.
- Security vulnerability: use the private process in [SECURITY.md](./SECURITY.md), never a public Issue.

Questions without enough information to reproduce or understand the problem may be closed until the requested redacted details are provided.

## Common checks

- Certificate errors: verify the configured URL, IP/domain SAN, root CA fingerprint, and operating-system trust settings.
- `Node offline`: confirm the Node process is running and inspect Relay state with `node doctor`.
- `Runtime unavailable`: verify that Codex runs locally under the same user and review its compatibility result.
- Pairing failures: create a fresh short-lived pairing request and confirm it on the trusted Node.
- Workspace unavailable: recheck the local allowlist, canonical path, ownership, symlink boundary, and configured permissions.

Do not work around these errors by disabling TLS, hostname verification, signatures, secure storage, workspace checks, or approval validation.

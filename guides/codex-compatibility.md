# Codex and toolchain compatibility

Yuanshu does not hard-code one Codex, Node.js, pnpm, Go, or browser product version as a runtime allowlist.

- The Node detects `codex --version` and attempts app-server initialization for unknown versions.
- The compatibility matrix records tested combinations and release evidence; it does not reject unlisted versions.
- Missing or changed capabilities are reported as `unverified`, `partial`, `unavailable`, or a specific capability error.
- Yuanshu never fabricates history or bypasses local policy to make an unknown version appear compatible.

## Verified references

| Component | Reference versions | Meaning |
| --- | --- | --- |
| Codex CLI/app-server | `0.144.x`, `0.146.0-alpha.9.2`, and their tested ranges | Manual/automated reference, not a runtime restriction |
| Node.js | Version in `.node-version` | Web build reference |
| pnpm | Version installed by CI | Web dependency/build reference |
| Go | Version in `.go-version` | Go test/build reference |

When evaluating a newer Codex version:

1. Run `yuanshu node doctor --json`.
2. Verify initialize and authentication state.
3. Exercise Thread list/read/start, Turn streaming and recovery.
4. Exercise approval and Diff behavior when supported.
5. Report the redacted compatibility result and error codes if behavior differs.

Protocol v1, Yuanshu persistence schemas, and security policy remain Yuanshu's own compatibility boundaries. They are separate from the Codex version matrix.

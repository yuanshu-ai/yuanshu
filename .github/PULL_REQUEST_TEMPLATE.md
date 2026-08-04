## Summary

Describe the user or engineering problem and the bounded change that solves it.

## Verification

- [ ] Added or updated behavior tests
- [ ] Ran the relevant Go/Web/Protocol checks
- [ ] Ran `git diff --check`
- [ ] Added screenshots for visible UI changes, or UI is unchanged

List the exact commands and results:

```text

```

## Security and data boundaries

- [ ] No credentials, keys, cookies, private task content, full command output, Diffs, absolute local paths, or private infrastructure details are committed
- [ ] Node remains the final Agent Runtime and workspace security boundary
- [ ] Server task-content storage behavior is unchanged, or the change is explicitly justified
- [ ] Reconnect and retry behavior cannot duplicate side-effecting controls

Describe any security, privacy, migration, or compatibility effect. Write `None` only after checking.

## Architecture checklist

- [ ] Protocol or generated wire types are unchanged, or Schema, fixtures, generated files, and compatibility tests are included
- [ ] Persistence is unchanged, or migration, rollback, and compatibility tests are included
- [ ] Public API or accepted ADR behavior is unchanged, or the design decision is linked
- [ ] Platform-specific behavior remains behind Platform/build-tag boundaries
- [ ] Separate `yuanshu-ai/docs` changes are linked when required

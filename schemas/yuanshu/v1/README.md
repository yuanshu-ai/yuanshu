# Yuanshu Protocol v1

`yuanshu-protocol.schema.json` is the sole source for the formal Yuanshu Protocol v1 wire types. The committed Go and TypeScript files are generated artifacts and must not be edited manually.

Protocol `1.0` uses JSON Schema 2020-12. Control frames are limited to 256 KiB and event frames to 1 MiB. IDs are opaque strings, timestamps are RFC 3339, and sequence values stay within the JavaScript safe-integer range.

Run from the repository root:

```shell
pnpm protocol:generate
pnpm protocol:check
pnpm protocol:test
```

ADR-013 fixes control signing to `Ed25519(UTF8("yuanshu-control-v1\\0") || RFC8785-JCS(controlWithoutSignature))`. Signatures are unpadded Base64url. Approval operation digests use SHA-256 over the separately domain-separated stable approval binding. Shared public test vectors live in `fixtures/signing-vectors.json`.

This package only defines deterministic encoding and digest helpers. Trust lookup, signature verification, TTL, nonce, sequence, replay, revocation, and key storage belong to later protocol-security tasks.

# Yuanshu Protocol v1

`yuanshu-protocol.schema.json` is the sole source for the formal Yuanshu Protocol v1 wire types. The committed Go and TypeScript files are generated artifacts and must not be edited manually.

Protocol `1.0` uses JSON Schema 2020-12. Control frames are limited to 256 KiB and event frames to 1 MiB. IDs are opaque strings, timestamps are RFC 3339, and sequence values stay within the JavaScript safe-integer range.

Run from the repository root:

```shell
pnpm protocol:generate
pnpm protocol:check
pnpm protocol:test
```

The signature and `operationDigest` fields are opaque in this baseline. Their canonical byte encoding and verification behavior remain an ADR-013 / AC-102 decision.

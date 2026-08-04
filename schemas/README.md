# Schemas

This directory contains versioned Yuanshu Protocol and configuration schemas with shared compatibility fixtures.

- `yuanshu/v1` is the formal wire protocol source.
- `config/v1/node-config.schema.json` defines the strict TOML-backed Node configuration model.
- `config/v1/server-config.schema.json` defines Server config v2, including the four deployment modes, while the runtime retains documented v1 compatibility.

Protocol and generated types must change together. Use the repository scripts rather than editing generated Go or TypeScript files directly:

```shell
pnpm protocol:generate
pnpm protocol:check
pnpm protocol:test
```

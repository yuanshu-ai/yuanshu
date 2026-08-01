# Yuanshu Node Configuration v1

`node-config.schema.json` describes the JSON data model represented on disk as
strict TOML. The Go implementation rejects unknown TOML fields and applies
additional semantic checks for credential-free URLs, duplicate workspace IDs
and paths, and local-only MVP runtime modes.

Configuration files contain only opaque SecretRef values. They never contain
device private keys, Relay tokens, proxy credentials, or Agent credentials.

The first published version is `config_version = 1`. No fictional v0 format is
defined; future versions must add an explicit sequential migration and fixture.

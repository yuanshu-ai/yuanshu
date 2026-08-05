# Yuanshu Node configuration v2

Version 2 replaces the single Codex adapter block with versioned Agent Instances and binds each workspace to one or more managed instances. Version 1 remains readable and is normalized in memory to `codex-default`; ordinary startup does not rewrite the source file.

Credentials, provider API keys, custom headers, native session identifiers, and absolute paths are never part of the remotely visible Agent projection.

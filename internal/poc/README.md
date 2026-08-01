# M0 PoC boundary

This directory contains the disposable `m0-poc-1` vertical proof of concept
used for Gate G0. It is intentionally internal and not a stable Yuanshu API.

The trust path is fixed:

```text
browser -> loopback HTTPS/WSS Server -> outbound-WSS Node -> Codex stdio Probe
```

The Server only authenticates and routes bounded frames. Agent credentials,
workspace paths, raw app-server request IDs, and Runtime access remain inside
the Node. `StandaloneTransport` changes only the Server-to-Node link to an
in-process bounded channel; it still uses the same Node controller.

Do not deploy this PoC on a LAN or the public internet. Formal pairing,
signed controls, persistence, production certificates, and the stable
CodexAdapter are intentionally deferred to later tasks.

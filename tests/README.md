# Tests

Cross-package contract, integration, platform, security, and proof-of-concept tests live here. Package-level unit tests stay next to their source.

## Layout

- `contract/` verifies shared boundaries such as Protocol, configuration, Platform, Transport, Node, and Server behavior.
- `integration/` exercises multiple production components together.
- `fixtures/` contains bounded, non-secret test data.

Run the Go suites from the repository root:

```shell
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

## M0 Codex proof of concept

The isolated M0 PoC remains a developer-only regression harness. It is not a configuration path for the formal `yuanshu server`, `yuanshu node`, or `yuanshu standalone` commands.

Use only disposable loopback resources:

```text
YUANSHU_POC_LISTEN=127.0.0.1:7443
YUANSHU_POC_TLS_CERT=<localhost certificate PEM>
YUANSHU_POC_TLS_KEY=<localhost private key PEM>
YUANSHU_POC_NODE_TOKEN=<at least 32 random bytes>
YUANSHU_POC_SERVER_URL=wss://localhost:7443
YUANSHU_POC_WORKSPACE=<existing non-root disposable directory>
```

`YUANSHU_POC_ARCHIVE_ON_CLOSE=1` is reserved for bounded test runs. Never reuse the PoC token or certificate, point the workspace at real private source, or expose the PoC outside loopback.

The formal implementation uses Protocol v1, signed controls, the production Node policy boundary, and the four documented Server deployment modes. New product behavior must be tested through those formal components rather than added to the PoC.

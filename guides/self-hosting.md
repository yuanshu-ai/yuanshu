# Self-hosting and LAN TLS

Yuanshu supports direct IP access without requiring a domain. Remote access still uses HTTPS/WSS; only literal `127.0.0.1` and `::1` may use HTTP/WS.

## Choose a mode

| Mode | Use | Trust model |
| --- | --- | --- |
| `local` | Server, browser, and Node on one computer | Literal loopback only; no certificate |
| `lan-managed` | Home or office private network | Per-Server CA installed explicitly on each device |
| `public-ip-acme` | Fixed globally routable IP | Automated short-lived ACME certificate and public TCP 443 |
| `external` | Existing domain, PKI, Caddy, or Nginx | User certificate or same-host loopback proxy |

Start with the local setup wizard:

```shell
yuanshu server setup --config /absolute/path/server.toml
```

It listens on a random loopback port, uses a one-time local session, validates the selected mode, and writes configuration and certificate state atomically. A running Server permits inspection but not mode switching.

## Local evaluation

Select `local` and use `127.0.0.1` or `::1`. Yuanshu validates the exact listener, Host header, and loopback peer; hostnames, private IPs, wildcard addresses, and proxy networks do not receive the plaintext exception.

## LAN Managed

Choose a stable private IPv4 address or ULA IPv6 address. Yuanshu creates:

- one ECDSA P-256 root CA for that Server;
- a 90-day leaf certificate with the selected IP SAN;
- automatic leaf renewal before 30 days remain;
- `/trust` installation instructions and `/v1/trust/ca.crt` for the public root.

On the Server computer, verify the displayed SHA-256 root fingerprint. On every browser or Node device, verify the same fingerprint before installing or importing the root CA.

- iPhone/iPad: install the profile, then enable full trust in Certificate Trust Settings.
- Android: install it as a CA certificate through the device security settings.
- macOS: import it into the current user's Keychain and set trust explicitly.
- Windows: import it into the current user's Trusted Root Certification Authorities.

After trust is enabled, reopen the HTTPS workbench and test WSS connectivity. Node setup can import the public CA PEM into the Node's private directory. It adds that CA to the system pool without disabling hostname or IP verification.

The CA private key is never served over HTTP and is not included in ordinary Server database backups. Back it up separately:

```shell
yuanshu server cert backup-ca --config /absolute/path/server.toml --output /private/backup/yuanshu-ca.age
yuanshu server cert restore-ca --config /absolute/path/server.toml --from /private/backup/yuanshu-ca.age
```

The recovery bundle uses age/scrypt. The passphrase comes from a TTY or a `0600` passphrase file, never argv or an environment variable. Restore requires the Server to be stopped.

## Public IP ACME

`public-ip-acme` accepts only a globally routable literal IP and an HTTPS public URL on port 443. Public TCP 443 must reach the Server, directly or through router port forwarding. Yuanshu uses the ACME `shortlived` profile and TLS-ALPN-01, renews automatically, and fails closed when no valid normal certificate remains.

Use ACME staging first. Real staging and production issuance remain manual acceptance gates for the pre-alpha.

## External certificates or proxy

`external/server` loads a matching certificate and private key from absolute local paths, validates SAN and permissions, and polls for atomic certificate replacement. Invalid replacement files do not displace a still-valid certificate.

`external/proxy` requires the proxy and Yuanshu to share one host. Yuanshu listens only on literal loopback, while the browser-facing `public_url` remains HTTPS. Preserve Host, Origin, and WebSocket Upgrade. Yuanshu never trusts forwarded Owner or Client identity headers.

## Diagnostics

```shell
yuanshu server doctor --config /absolute/path/server.toml --json
yuanshu server cert status --config /absolute/path/server.toml --json
```

Doctor reports the deployment mode, Provider, SAN, fingerprints, expiry, renewal state, Origin configuration, and redacted errors. It never returns certificate private keys or full private file paths.

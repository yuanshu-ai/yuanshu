# Security Policy

Yuanshu controls coding agents on user-owned computers. Identity, workspace, credential, approval, and transport failures can therefore have serious consequences. Please report suspected vulnerabilities privately and avoid testing against systems or data you do not own.

## Supported versions

Yuanshu is pre-alpha and has no supported binary release. Security reports against the current `main` branch are accepted on a best-effort basis. Older commits, forks, development fixtures, and unreleased artifacts are not maintained as supported versions.

## Private reporting

Use [GitHub Private Vulnerability Reporting](https://github.com/yuanshu-ai/yuanshu/security/advisories/new). Do not open a public Issue, Discussion, or pull request for an undisclosed vulnerability.

Include only the minimum information needed to reproduce the problem:

- affected commit and platform;
- deployment mode and component;
- impact and required attacker access;
- bounded reproduction steps or a minimal proof of concept;
- relevant redacted error codes or correlation IDs;
- suggested mitigation, if known.

Never include live credentials, private keys, API keys, authentication cookies, private task content, complete command output, complete Diffs, absolute workspace paths, private Relay addresses, or another person's data.

Maintainers will review reports as capacity allows, coordinate validation and remediation privately, and publish details only after an appropriate fix or mitigation is available. The project does not yet promise a fixed response or remediation SLA.

## High-priority areas

Reports are especially valuable when they involve:

- authentication, signing, key rotation, revocation, or replay protection;
- cross-Owner, cross-Node, cross-workspace, or cross-Thread isolation;
- workspace path traversal, symlink boundaries, permission escalation, or arbitrary command execution outside an allowed workspace;
- approval binding, operation digest, lease, expiry, or duplicate-execution bypasses;
- TLS, certificate, managed CA, ACME, Origin, Host, Cookie, CSRF, or local IPC boundaries;
- credential, private key, Prompt, command output, Diff, or local path disclosure;
- Server storage of task content contrary to the documented data boundary.

## Safe testing expectations

- Test only on systems, accounts, workspaces, and networks you control.
- Use disposable data and credentials.
- Do not degrade availability for other users or attempt persistence.
- Stop after demonstrating the minimum impact needed for a report.
- Allow maintainers reasonable time to investigate before disclosure.

Good-faith research that follows these expectations is welcome. This policy is not authorization to access third-party systems and does not waive applicable law.

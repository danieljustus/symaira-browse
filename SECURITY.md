# Security Policy

## Supported versions

Only the latest version on the default branch is currently supported with security fixes while the project is in its foundation phase.

## Reporting a vulnerability

Please report suspected vulnerabilities privately to the project maintainers rather than opening a public issue. Include:

- A concise description of the affected behavior.
- The versions or commit identifiers affected.
- Reproduction steps or a minimal proof of concept, if safe to share.
- The potential impact and any suggested mitigation.

Do not include passwords, API keys, session cookies, or other live credentials in a report. If a sensitive attachment is necessary, request a secure submission channel first.

The maintainers will acknowledge receipt, investigate, coordinate a fix, and communicate a disclosure timeline when appropriate. Please allow reasonable time for remediation before public disclosure.

## Security boundaries

Symaira Browse treats web pages as untrusted input. Do not bypass authentication, CAPTCHA, domain policy, or other access controls while testing without explicit authorization. Follow the repository rules in [AGENTS.md](AGENTS.md) and the security boundaries documented in [docs/ssrf.md](docs/ssrf.md), [docs/allowlist.md](docs/allowlist.md), and [docs/injection.md](docs/injection.md).

## Code scanning

CodeQL analysis runs as a custom workflow (`.github/workflows/ci.yml`, job
`CodeQL / analysis`) for the Go codebase on every pull request touching code
and weekly on the default branch. This is the intentional setup — GitHub
code-scanning default setup is deliberately not enabled so the analysis
configuration stays versioned with the repository. Findings appear in the
repository's Security tab and block pull requests.

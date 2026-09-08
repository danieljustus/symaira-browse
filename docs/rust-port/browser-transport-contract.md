# Browser transport contract

## Purpose

The transport selected for a request is a truthful capability statement, not a
cosmetic profile name. Browser identity, transport fingerprint and browser
automation are separate concerns.

## Modes

| Mode | Meaning | Prohibited claim |
|---|---|---|
| `static` | The owned Rust HTTP transport for documents and APIs. | That it is Chrome, Safari, Firefox, or an impersonated browser. |
| `browser` | The requested installed local engine executes the request. Supported engines are `chrome`, `safari` and `firefox`. | That a different engine or generic HTTP stack was used. |
| `compat` | The temporary, pinned Go/AzureTLS legacy transport. | That its frozen profile is the installed browser's present version or state. |

Mode and, for browser mode, engine selection are explicit. A request has exactly
one mode. A requested unavailable mode/engine returns a stable typed error; no
implementation may silently substitute `static`, `compat`, Chrome, Safari or
Firefox.

## Browser-mode acceptance rules

Each engine has a separate native gate. It must prove local discovery or
explicit attach, prerequisite diagnostics, navigation and redirects, JavaScript,
cookies and storage, response/download capture where supported, timeout, and
owned-process cleanup. Capability reporting is engine-specific: unimplemented
operations fail explicitly rather than returning fabricated partial results.

- **Chrome:** local Chrome/Chromium through its CDP-compatible adapter.
- **Safari:** macOS-only local Safari through Apple Events and/or
  SafariDriver/WebDriver BiDi. Remote Automation is a documented prerequisite,
  not a hidden setup action.
- **Firefox:** local Firefox through WebDriver/BiDi on macOS, Linux and Windows.

Browser mode relies on the real engine's version, network stack, cookies,
storage and JavaScript state. It is tested functionally against a hermetic
fixture server; it does not promise a frozen JA3/JA4 string.

## Compatibility mode

`compat` remains until a replacement demonstrates the legacy six-profile wire
fixture without unacceptable dependencies. Rust communicates with the Go
component through a versioned local protocol that includes request IDs, bounded
timeouts, typed errors, private endpoint permissions, restart/exit behavior and
rollback. Compatibility-mode failure never changes the requested mode.

## Cutover

Release approval requires all static, compat and individual Chrome/Safari/
Firefox contract rows to pass on their declared platforms. Safari is only
advertised where its macOS gate passes. A mode is not globally advertised based
on another engine's result.

## Selection implementation (Phase 5A)

Rust resolves `SYMBROWSE_MODE`/`mode` and `SYMBROWSE_ENGINE`/`engine` with
flag-over-file precedence. The typed selection is shared by the CLI, daemon
and MCP autostart path. Browser mode requires one of `chrome`, `safari` or
`firefox`; an engine is rejected in static or compat mode. Unknown and
unavailable selections are typed failures, never substitutions. The
`version --json` handshake is unchanged; transport metadata is confined to
ordinary operation results.

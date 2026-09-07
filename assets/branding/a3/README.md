# Approved A3 branding handoff

This directory vendors the approved Symaira A3 (Verzahnt) brand package for Symaira Browse.

- Role: brand-only asset handoff for a CLI/MCP/browser-infrastructure repository.
- No GUI target, application bundle, or CLI behavior is introduced.
- The existing Symaira Browse wordmark and product identity remain unchanged.
- Source handoff: `symaira-icons-release/symaira-browse/`.
- The `.icon` package is the editable Icon Composer master; the PNG is an opaque branding export for documentation and release communication.
- The package is not currently consumed by a native app bundle or automatically embedded in CLI releases.

The committed manifest records the approved product signet, source handoff, and SHA-256 values for every vendored file. The small asset test validates those records in normal CI.

# Contributing to Symaira Browse

Thanks for helping improve Symaira Browse. Please read [AGENTS.md](AGENTS.md) before making a change; it is the binding design contract, and the per-topic documents under [docs/](docs) are its detailed reference. The issue or milestone defines the permitted scope.

## Development workflow

1. Create a focused branch from the current default branch.
2. Keep the change limited to one issue when practical.
3. Add tests for behavior changes and preserve standalone-first operation.
4. Run the required checks before opening a pull request:

   ```sh
   make fmt-check
   make build
   make test
   make test-race
   make lint
   ```

5. Do not commit binaries, credentials, local configuration, or generated reports.
6. Describe the behavior change, checks run, and any residual risk in the pull request.

## Design boundaries

- Keep the core build CGO-free and on Go 1.26.5.
- Do not add compile-time dependencies on sibling Symaira repositories.
- Preserve stable JSON output contracts as they are introduced.
- Treat browser content as untrusted input and keep policy decisions explicit.

For security-sensitive changes, follow [SECURITY.md](SECURITY.md) instead of opening a public issue with exploit details.

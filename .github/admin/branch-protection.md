# Branch protection note

When configuring branch protection for `main`, require only these pull-request checks:

- `PR / lint`
- `PR / test (ubuntu)`

Do not require `Main / full (...)` matrix checks or `CodeQL / scheduled`; those jobs run only on `main` pushes and the weekly schedule.

The CI workflow uses `paths-ignore` for `docs/**`, `**.md`, `LICENSE`, and `.github/ISSUE_TEMPLATE/**` on both `push` and `pull_request`. Documentation-only changes intentionally do not start CI, so GitHub's behavior for skipped required checks must be accounted for when applying branch-protection rules.

This note documents the intended check names only; it does not change repository settings.

# Web-form automation contract (formflow)

The `formflow` package is the consumable web-form automation surface of
symbrowse for downstream Symaira products (issue #280). First consumer:
[symaira-eraseme](https://github.com/danieljustus/symaira-eraseme) (Go
rewrite, tracking issue #732; consumption tracked in #719).

## Decision: in-process Go API

`formflow` is a **public, in-process Go package**
(`github.com/danieljustus/symaira-browse/formflow`), consumed as a normal Go
module dependency — the same pattern symaira-corekit already uses across the
product family. It is *not* a CLI subprocess wrapper and it does *not*
require the symbrowse daemon.

Rationale:

- Keeps `brew install symeraseme` a single-binary install (a stated goal of
  the EraseMe port).
- Typed outcomes instead of stdout parsing or string matching, which would
  rot as the CLI evolves.
- Chrome access stays behind symbrowse's engine boundary; the only runtime
  requirement of the consumer binary is a Chrome/Chromium installation.

The daemon and CLI keep working unchanged; `formflow` is an additional,
additive surface.

## Surface

```go
runner := formflow.NewRunner(driver)            // driver: formflow.Driver
result, err := runner.SubmitForm(ctx, spec)     // spec: formflow.FormSpec
result, err := runner.ConfirmLink(ctx, cspec)   // cspec: formflow.ConfirmationSpec
```

- `FormSpec` — start URL, field map (`[]Field` with semantic-first
  `Selector`: label/placeholder/testid/role/text, CSS as fallback), submit
  selector, optional success-URL glob, bounded per-run timeout
  (`DefaultRunTimeout` = 60s when unset).
- `Driver` — narrow browser interface. `NewEngineDriver(browser, page)`
  adapts a live symbrowse engine page; tests can inject fakes.
- `Result` — machine-readable outcome: `code`, `message`, `failed_step`,
  `failed_field`, `hint`, `skipped_fields`, `evidence`, `duration_ms`. The
  JSON field schema is stable (pinned by test).

## Outcome taxonomy

Consumers **switch on `Result.Code`**, never on message text:

| Code | Meaning | Consumer action |
|---|---|---|
| `success` | flow completed, evidence captured | store evidence, mark request submitted |
| `invalid_spec` | spec failed validation pre-flight | fix the field map |
| `navigation_timeout` | page did not load in time | retry policy of the consumer |
| `form_not_found` | no fillable form on the page | update broker definition |
| `field_not_found` | a **required** field could not be located | update broker definition — request was NOT submitted |
| `blocked_captcha` | CAPTCHA detected (widget or challenge text) | route to human task queue |
| `blocked_botwall` | non-CAPTCHA bot wall (browser check, rate page, access denial) | route to human task queue |
| `interaction_failed` | fill/click failed (overlay, detached node, ...) | investigate broker definition |
| `submit_failed` | submit or the contracted success URL failed | treat as NOT submitted |
| `confirmation_failed` | confirmation flow reached no verifiable confirmed state | handle manually |

Codes only ever grow additively. CAPTCHA solving is a deliberate non-goal:
detection and clean handoff only.

## Loud failure guarantee

A broker renaming a field must never produce a silently half-filled GDPR
erasure request. Required fields that cannot be located abort the run with
`field_not_found` **before** the submit click; optional fields are skipped
and listed in `skipped_fields`.

## Evidence capture (compliance trail)

Deterministic capture points:

1. **pre-submit screenshot** — immediately before the submit click;
2. **post-submit screenshot** — after the post-submit wait (success URL or
   network idle) resolved;
3. **final URL + visible page text** — at the same point.

Failures capture best-effort evidence (final URL, page text, screenshot) so
the trail shows what the page actually displayed. Screenshots marshal as
base64 in JSON.

## Confirmation links

`ConfirmLink` opens a confirmation link from a broker email, finds the
confirmation control (German and English candidates, overridable), clicks
through, verifies the optional contracted success URL, and reports
`success` / `confirmation_failed` / `blocked_*` with evidence.

## Respectful pacing

`Pacer` enforces a minimum interval per host (default 5s,
`DefaultMinHostInterval`). Campaigns across many brokers keep full speed;
repeated hits on one host are serialized. Override via `NewPacer(interval)`
or disable by leaving `Runner.Pacer` nil (not recommended for campaigns).

## Risk classes

Prepared as constants per the repository risk policy (enforcement starts in
milestone M4): navigation → `ClassForCommand("open")`, fill → `ClassInteract`,
submit → `ClassSubmit`, evidence capture → `ClassRead` (`formflow.RiskNavigate`,
`RiskFill`, `RiskSubmit`, `RiskEvidence`).

## Test corpus

`internal/testserver` ships a hostile-form corpus (issue #281): misleading
labels + honeypot (`/form-hostile`), CAPTCHA gate (`/form-captcha`), bot
wall (`/botwall`), confirmation flow (`/confirm`, `/confirm/done`).
Classification of every corpus page is pinned by tests in `formflow`.

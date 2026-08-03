# Local browser fixture server

`internal/testserver` provides deterministic pages for browser and CDP tests. It
uses Go's `httptest.NewServer`, so every instance starts in-process on an
operating-system-assigned loopback port. It never binds a fixed port or makes
an external network request.

## Usage

```go
server := testserver.New(t) // t.Cleanup(server.Close) is registered
pageURL := server.URLFor(testserver.Form)

// Or manage the lifecycle explicitly:
server := testserver.New()
defer server.Close()
pageURL := server.BaseURL + testserver.PathFor(testserver.Static)
```

`Server.URL` and `Server.BaseURL` contain the same absolute base URL. `Routes`
returns the complete stable fixture registry, and `PathFor` returns a stable
path without requiring a server instance.

## Fixture routes

| Fixture | Route | Behavior and intended test usage |
|---|---|---|
| `Static` | `/static` | Self-contained static HTML document with a heading, stable content ID, and a link. Use for basic navigation, text, HTML, and deterministic snapshot tests. |
| `Form` | `/form` | Multipart form containing a text input (`name="text"`), select (`name="select"`), checkbox, radio group, file input, and submit button. Use for input discovery and form interaction tests. |
| `SPA` | `/spa` | Initially serves `Loading application…` with `data-hydrated="false"`. Inline JavaScript hydrates after 75 ms, changes the marker to `true`, adds `Hydrated application content`, and creates `#hydrated-button`. Use for wait and delayed-render tests. |
| `Overlay` | `/overlay` | A fixed backdrop and modal (`role="dialog"`) are above an underlying button. The backdrop remains until `#close-modal` is clicked, so it intercepts clicks intended for `#underlying-button`. Use for obscured-click and modal tests. |
| `Iframe` | `/iframe` | Parent document containing `#child-frame`, which loads `/iframe/child`. The child contains `#grandchild-frame`, which loads `/iframe/grandchild`. Use for nested frame traversal and frame-boundary tests. |
| `ShadowDOM` | `/shadow-dom` | Defines a custom element with an open shadow root containing `#shadow-content` and `#shadow-button`. Use for shadow-root discovery and interaction tests. |
| `HiddenText` | `/hidden-text` | Five distinct variants: `display: none` (`#display-none`), `visibility: hidden` (`#visibility-hidden`), zero font size (`#font-size-zero`), zero opacity (`#opacity-zero`), and off-viewport positioning (`#offscreen`). Use for visibility and hidden-content detection tests. |
| `AriaLabelMismatch` | `/aria-label-mismatch` | Button visibly says `Continue` but has `aria-label="Delete account"`. Use for accessible-name mismatch and prompt-injection warning tests. |
| `PromptInjection` | `/prompt-injection` | Agent-directed imperatives in visible text, hidden text (`display:none`), `alt`/`title` attributes, an HTML comment, and `<meta>` content. Use for prompt-injection scanner tests (issue #28). |
| `RedirectLoop` | `/redirect-loop` | Redirects to `/redirect-loop/a`; `/redirect-loop/a` redirects to `/redirect-loop/b`; `/redirect-loop/b` redirects back to `/redirect-loop/a`. Use with a client redirect limit or disabled redirect following to test loop handling. |
| `Slow` | `/slow` | Waits exactly `SlowResponseDelay` (currently 100 ms), unless the request context is canceled, then returns without writing. Use for timeout and cancellation tests. |
| `NotFound` | `/not-found` | Explicit HTTP 404 response with a deterministic plain-text error body. |
| `InternalServerError` | `/server-error` | Explicit HTTP 500 response with a deterministic plain-text error body. |
| `MarkerSpoof` | `/marker-spoof` | Page whose content mimics the symbrowse content-boundary marker lines with a forged nonce. Use for the boundary unforgeability test (read pipeline must keep the forged markers inside the content). |

The root route `/` is a small index linking to every registered fixture. The
nested iframe support routes `/iframe/child` and `/iframe/grandchild`, and the
redirect endpoints `/redirect-loop/a` and `/redirect-loop/b`, are stable too
but intentionally remain implementation routes rather than top-level fixture
registry entries.

## Lifecycle and isolation

Use one server per test or test group and close it with `Server.Close`. The
optional `testing.TB` argument to `New` registers cleanup automatically. Each
call to `New` gets a separate ephemeral address; callers must use `URL`,
`BaseURL`, or `URLFor` rather than hard-coding a port.

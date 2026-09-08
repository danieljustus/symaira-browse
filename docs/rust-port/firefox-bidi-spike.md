# Firefox WebDriver BiDi spike

Executed on macOS 27.0 arm64 with `/Applications/Firefox.app/Contents/MacOS/firefox` (Firefox 152.0.6).

The maintained transport dependency is the workspace-pinned `async-tungstenite` crate,
used directly by the owned adapter; no geckodriver dependency is required. Firefox
was launched headless with `--remote-debugging-port 9222` and a profile created under
`$TMPDIR/symbrowse-firefox-spike-*`. Its stderr reported:

```
WebDriver BiDi listening on ws://127.0.0.1:9222
```

The first probe verified that a fixed loopback port is required: Firefox advertises
the endpoint in its process output rather than `/json/version`. The adapter therefore
allocates a free loopback port, waits for TCP readiness with a bounded timeout, and
connects only to `127.0.0.1`. It sends `session.new`, `browsingContext.getTree`, and
supports navigation, `script.evaluate`, cookies/storage, click/type interactions,
browsing-context tab/frame enumeration, and viewport PNG/JPEG screenshots. Downloads
and response capture remain typed unsupported; no command claims those capabilities.

Cleanup evidence: the probe used a unique temporary profile, sent termination to the
owned process group, waited for the direct child, and removed only that profile. The
Rust adapter additionally uses `kill_on_drop` and waits after explicit close. No user
Firefox profile was opened or modified. Firefox emitted sandbox/plugin warnings on this
host, but the BiDi endpoint became ready and remained local.

The native probe is opt-in (`SYMBROWSE_E2E=1`) and intentionally emits diagnostics on
stderr; normal daemon stdout remains reserved for protocol frames.

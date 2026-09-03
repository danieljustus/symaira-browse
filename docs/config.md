# Configuration

`symbrowse` reads configuration from built-in defaults, the global TOML file,
the project TOML file, and `SYMBROWSE_*` environment variables. Explicit command
flags win over environment variables; environment variables win over project
TOML; project TOML wins over global TOML; global TOML wins over defaults.
`config show` reports the effective value and its source without exposing secret
material.

Global configuration is read from `$XDG_CONFIG_HOME/symbrowse/config.toml` when
`XDG_CONFIG_HOME` is set, otherwise from `$HOME/.config/symbrowse/config.toml`.
Cache and state defaults follow `XDG_CACHE_HOME` and `XDG_STATE_HOME` in the same
way.

## Runtime environment

| Variable | Default | Source / precedence |
|---|---|---|
| `SYMBROWSE_LOG_LEVEL` | `warn` | env over TOML/default; also consumed by logging initialization |
| `SYMBROWSE_LOG_FORMAT` | `text` | env over TOML/default; also consumed by logging initialization |
| `SYMBROWSE_CONFIG_DIR` | `$XDG_CONFIG_HOME/symbrowse` or `$HOME/.config/symbrowse` | env over TOML/default |
| `SYMBROWSE_CACHE_DIR` | `$XDG_CACHE_HOME/symbrowse` or `$HOME/.cache/symbrowse` | env over TOML/default |
| `SYMBROWSE_STATE_DIR` | `$XDG_STATE_HOME/symbrowse` or `$HOME/.local/state/symbrowse` | env over TOML/default; owns states, journal and the default daemon log |
| `SYMBROWSE_EXECUTABLE_PATH` | empty; platform discovery | env over TOML/default |
| `SYMBROWSE_CDP_ENDPOINT` | empty; launch a private browser | env over TOML/default; `--cdp-endpoint` wins |
| `SYMBROWSE_ENGINE` | `chrome` | env over TOML/default; `--engine` wins; one of `chrome`, `static`, `safari-attach`, `safari-bidi` |
| `SYMBROWSE_ALLOWED_DOMAINS` | empty; no domain allowlist | env over TOML/default; `--allowed-domains` wins |
| `SYMBROWSE_SSRF` | `false` for the daemon | env over TOML/default; `--ssrf` wins |
| `SYMBROWSE_ALLOW_PRIVATE` | `false` | env over TOML/default; `--allow-private` wins |
| `SYMBROWSE_HEADLESS` | `false` | env over TOML/default; `--headless` wins |
| `SYMBROWSE_CACHE_TTL_HOURS` | `24` | env over TOML/default; applies to fetch responses and unified output handles |
| `SYMBROWSE_FETCH_ROBOTS` | `true` | env over TOML/default; fetch checks robots.txt before the page request |
| `SYMBROWSE_FETCH_USER_AGENT` | `symbrowse/1.0` | env over TOML/default; the same value is used for robots matching and fetch requests |
| `SYMBROWSE_FETCH_NO_CACHE` | `false` | env over TOML/default; forces fresh fetches unless a request explicitly overrides it |
| `SYMBROWSE_IDLE_TIMEOUT` | `1800` seconds; `0` disables idle expiry | env over TOML/default |
| `SYMBROWSE_OPERATION_TIMEOUT` | `25` seconds | env over TOML/default |
| `SYMBROWSE_READ_TIMEOUT` | `30` seconds | env over TOML/default |
| `SYMBROWSE_STATE_EXPIRE_DAYS` | `30` days | env over TOML/default |
| `SYMBROWSE_AUTOSAVE` | `auto` | env over TOML/default; `auto`, `always` or `never` |
| `SYMBROWSE_AUTOSAVE_INTERVAL` | `30` seconds | env over TOML/default; `0` saves only on close |
| `SYMBROWSE_AUTOSAVE_KEY` | empty; autosave disabled without a restore key | env over TOML/default |
| `SYMBROWSE_UPLOAD_DIRS` | current working directory | env over TOML/default; comma-separated roots |
| `SYMBROWSE_DAEMON_LOG` | `<state_dir>/daemon.log` | env over the resolved state directory; explicit path wins |
| `SYMBROWSE_APPROVAL_TIMEOUT` | `60` seconds | env over TOML/default |
| `SYMBROWSE_CHROME_STARTUP_TIMEOUT` | `10` seconds | env; Chrome startup tuning |
| `SYMBROWSE_CHECK_UPDATES` | unset/off | env; enables the asynchronous update hint |
| `SYMBROWSE_SYMGUARD` | unset; discover `symbrain` on `PATH` | env; external risk-decider override |
| `SYMBROWSE_ENCRYPTION_KEY` | unset | env fallback only; 64 hex characters (32 bytes), never shown by `config show` |

The first 26 settings in the table are represented in the effective `Config`
and appear in `symbrowse config show`. Secret values are intentionally excluded
from the output even though the encryption-key variable is documented here.
The two process controls below remain raw environment controls by design:

| Variable | Default | Behavior |
|---|---|---|
| `SYMBROWSE_MCP` | unset/off | selects MCP policy mode for a daemon process |
| `SYMBROWSE_NO_AUTOSTART` | unset/off | prevents CLI clients from starting a daemon |

## Test and protocol variables

These are not user configuration settings:

- `SYMBROWSE_E2E` opts into the real-Chrome E2E suite.
- `SYMBROWSE_HEADED` opts the E2E suite into a visible browser.
- `SYMBROWSE_CHROME_HELPER` starts the internal Chrome test helper.
- `SYMBROWSE_CONTENT_START` and `SYMBROWSE_CONTENT_END` delimit trusted content
  in the internal test fixture protocol.

## Fetch settings in `config.toml`

The corresponding TOML keys use the same names without the `SYMBROWSE_`
prefix:

```toml
cache_ttl_hours = 24
fetch_robots = true
fetch_user_agent = "symbrowse/1.0"
fetch_no_cache = false
```

The normal precedence chain applies: project TOML overrides global TOML,
`SYMBROWSE_*` overrides both, and explicit command flags win where a command
provides one. `fetch_no_cache` disables the fetch response cache by default;
`store_full_text` output handles remain available for explicit retrieval.

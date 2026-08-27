# SSRF-Guard

Der SSRF-Guard verhindert, dass `symbrowse` (und damit ein Agent) interne
Adressen öffnet — klassische Server-Side-Request-Forgery-Angriffe. Er ist die
zweite Netzwerk-Policy neben der [Domain-Allowlist](allowlist.md).

## Defaults

| Modus | SSRF-Guard | Opt-out |
|---|---|---|
| `symbrowse mcp` (MCP-Modus) | **an (deny-by-default)** | `--allow-private` |
| `symbrowse daemon` (regulär) | aus (opt-in) | `--ssrf` aktiviert |

Der MCP-Modus öffnet den Netzwerk-Layer für Agenten und ist deshalb
deny-by-default: `http://localhost:…` oder interne IPs sind ohne explizites
Opt-in nicht erreichbar. Der reguläre Daemon bleibt unverändert offen, damit
lokale Workflows (z. B. Entwicklungs-Server auf `localhost`) nicht
regressieren; `--ssrf` aktiviert den Guard auch dort.

Aktivierungskette (wie bei der Allowlist): Flag → `SYMBROWSE_SSRF` /
`SYMBROWSE_ALLOW_PRIVATE` → `config.toml` (`ssrf_enabled`, `allow_private`).

## Blockierte Zieladressen

- Unspecified: `0.0.0.0/8`, `::/128`
- RFC1918: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`
- Loopback: `127.0.0.0/8`, `::1/128`
- Link-Local: `169.254.0.0/16`, `fe80::/10`
- Carrier-Grade-NAT: `100.64.0.0/10`
- IPv6 Unique-Local: `fc00::/7`
- `.local`-Namen (mDNS) und `localhost` (per Suffix, unabhängig von der
  Auflösung)
- IPv4-mapped IPv6 (`::ffff:0:0/96`) wird auf die IPv4-Klassifikation
  zurückgeführt.

Nicht-`http(s)`-Schemata und unparsbare URLs werden ebenfalls abgelehnt
(fail closed).

## DNS-Rebinding

Der Guard löst den Hostnamen **zum Entscheidungszeitpunkt** auf und prüft
jede aufgelöste Adresse: Antwortet ein Name (auch ein öffentlich wirkender)
mit einer privaten Adresse, wird die Anfrage blockiert. Schlägt die
Auflösung fehl, wird ebenfalls blockiert (fail closed) — ein
Rebinding-/NXDOMAIN-Bypass kommt nicht durch. Zusätzlich gilt die
Entscheidung für jede Anfrage neu (Request-Interception und Navigations-Gate
prüfen pro Request).

## Durchsetzung

Wie die Allowlist wird der Guard auf CDP-Ebene durchgesetzt:
`Fetch.requestPaused`-Interception (Subresources, Worker, Popups,
WebSockets, …) plus Fail-Fast-Gate in `Engine.Navigate` — eine blockierte
Navigation startet nicht einmal den Browser. Blockierte Anfragen werden pro
URL gezählt und als `warnings[]` gemeldet (`network_policy.blocked` mit
Begründung `denied by the SSRF guard`).

**Attach-Modus (`--cdp-endpoint` / `SYMBROWSE_CDP_ENDPOINT`, Issue #296):**
Der Guard greift nur für Requests, die durch die eigene CDP-Session laufen.
Requests des angehängten Browsers außerhalb dieser Session (andere Tabs,
Hintergrundaktivität des Menschen) sind nicht abgedeckt — der Daemon warnt
beim Start mit `network_policy.limitation`. Für garantierte Durchsetzung
einen eigenen Browser mit privatem Profil starten.

## Abweichungen zu `symfetch` (dokumentiert)

Der Guard ist bewusst mit `symfetch` (`internal/fetch/guard.go`) deckungsgleich
gebaut: dieselben privaten Bereiche, dieselbe Fail-closed-Auflösung, dasselbe
`AllowPrivate`-Opt-out. Dokumentierte Abweichungen:

1. **`.local`/`localhost`-Suffix-Sperre:** `symfetch` blockt `.local` nicht
   explizit. `symbrowse` blockt den Suffix zusätzlich, damit mDNS-Namen auch
   bei fehlschlagender Auflösung nicht durchrutschen.
2. **Default im Daemon-Modus:** `symfetch` hat den Guard grundsätzlich an
   (Opt-out per `AllowPrivate`). `symbrowse` hat ihn im regulären
   Daemon-Modus opt-in (`--ssrf`), um lokale Workflows und Fixture-Tests
   nicht zu brechen; der MCP-Modus ist wie bei `symfetch` deny-by-default.

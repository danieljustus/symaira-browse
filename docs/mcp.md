# MCP-Server (`symbrowse mcp`)

`symbrowse mcp` ist ein JSON-RPC-2.0-Server nach dem Model Context Protocol
(MCP) über stdio. Er macht den Browser für Agenten bedienbar: Tools proxen an
den lokalen `symbrowse`-Daemon, jede Sitzung ist isoliert, und die
Netzwerk-Policys (Domain-Allowlist + SSRF-Guard) gelten auch im MCP-Modus.

**Zero Stdio Pollution ist strikt:** Kein Byte auf stdout außer
JSON-RPC-Frames. Logs gehen ausschließlich nach stderr. Ein Tool-Call darf
den Handshake nie brechen (automatisierter Test in `internal/mcp`).

## Start

```sh
symbrowse mcp                          # Default-Profil core, SSRF-Guard an
symbrowse mcp --tools core,nav         # Profile kombinieren
symbrowse mcp --tools all              # volle Tool-Fläche
symbrowse mcp --list-profiles          # Profile + Tool-Anzahl anzeigen
symbrowse mcp --allow-private          # SSRF-Guard für private Ziele öffnen
```

Der Daemon wird bei Bedarf automatisch mit `--ssrf` gestartet (MCP-Default:
private/loopback-Ziele sind **deny**). Läuft bereits ein Daemon ohne
SSRF-Guard, warnt der Server beim Start — die Durchsetzung ist dann nicht
garantiert.

## Beispielkonfigurationen (alle real getestet)

Die `command`-Pfade sind Platzhalter — durch den absoluten Pfad des gebauten
Binaries ersetzen (z. B. `/Users/me/bin/symbrowse`). Jeder Schnipsel ist
validiertes JSON und wurde gegen das gebaute Binary geprüft
(`initialize` + `tools/list`-Handshake).

### Claude Code

```json
{
  "mcpServers": {
    "symbrowse": {
      "command": "/absoluter/pfad/zu/symbrowse",
      "args": ["mcp"]
    }
  }
}
```

Ablage als `.mcp.json` im Projekt-Root (oder `claude mcp add symbrowse --
/absoluter/pfad/zu/symbrowse mcp`).

### Cursor

```json
{
  "mcpServers": {
    "symbrowse": {
      "command": "/absoluter/pfad/zu/symbrowse",
      "args": ["mcp"]
    }
  }
}
```

Ablage als `.cursor/mcp.json` im Projekt-Root.

### OpenCode

```json
{
  "mcp": {
    "symbrowse": {
      "type": "stdio",
      "command": "/absoluter/pfad/zu/symbrowse",
      "args": ["mcp"]
    }
  }
}
```

Ablage als `opencode.json` im Projekt-Root (oder `opencode.json` im
Benutzerprofil für globale Verfügbarkeit).

### Claude Desktop

```json
{
  "mcpServers": {
    "symbrowse": {
      "command": "/absoluter/pfad/zu/symbrowse",
      "args": ["mcp"]
    }
  }
}
```

Ablage als `claude_desktop_config.json` (macOS:
`~/Library/Application Support/Claude/`).

## Tool-Profile

| Profil | Enthält | Anwendungsfall |
|---|---|---|
| `core` (Default) | `open`, `snapshot`, `click`, `fill`, `type`, `press`, `wait`, `read`, `get`, `find`, `fetch_url`, `fetch_batch`, `cache_get`, `wayback_snapshots` | Alltag: statisch holen, Seiten öffnen, ansehen, bedienen, lesen und gekürzte Ausgaben zurückholen |
| `nav` | `back`, `forward`, `reload` | Historien-Navigation |
| `state` | *(leer — kommt mit v0.4.0)* | Sessions, Cookies, Storage, `auth login` |
| `network` | *(leer — kommt mit v1.0.0)* | Routing, Mocking, HAR, Offline |
| `debug` | *(leer — kommt mit v1.0.0)* | Console, `eval`, `a11y`, `diff` |
| `flows` | *(leer — kommt mit v0.6.0)* | `flow list/run/record` |
| `all` | Vereinigung aller Profile | Volle Fläche, wenn der Agent sie braucht |

Empfehlungen: Standard-Agenten `core`; Aufgaben mit viel
Zurück/Weiter-Navigation `core,nav`; spezialisierte Agenten bekommen ihr
Profil, sobald die Tools der jeweiligen Milestone existieren. `all` nur,
wenn der Agent die volle Kontrolle benötigt — größere Tool-Fläche heißt
größere Angriffsfläche für prompt-injizierte Seiten.

Jedes Tool nimmt `session` entgegen (optional, Default: die Server-Session).
Eine Session pro Aufgabe verwenden; Sessions sind voneinander isoliert.

Für eine URL-Aufgabe zuerst `fetch_url` als Tier 0 verwenden. Für mehrere
unabhängige URLs `fetch_batch`, für historische Treffer
`wayback_snapshots`. Enthält die Antwort `escalate` oder braucht die Seite
JavaScript, Browser-Zustand oder Interaktion, auf `read` beziehungsweise den
interaktiven Ablauf mit `open` und `snapshot` eskalieren.

## Netzwerk-Sicherheit im MCP-Modus

- **SSRF-Guard: Default deny.** RFC1918, Loopback, Link-Local, `.local` und
  IPv6-ULA sind blockiert. `http://localhost:…` oder interne IPs öffnen
  ohne `--allow-private` nicht. Details: [docs/ssrf.md](ssrf.md).
- **Domain-Allowlist:** optional über den Daemon
  (`--allowed-domains`, `SYMBROWSE_ALLOWED_DOMAINS` oder `allowed_domains`
  in `config.toml`). Details: [docs/allowlist.md](allowlist.md).
- **Warnungen:** Blockierte Anfragen werden pro URL gezählt und auf dem
  Tool-Ergebnis gemeldet: Ist die Daemon-Antwort mit `warnings[]` behaftet,
  liefert das Tool `{"data": …, "warnings": […]}`. Agents sollen diese
  Warnungen ernst nehmen — sie zeigen an, was die Seite versucht hat zu
  laden.

## Durchgängiges Beispiel

Aufgabe: hinter einer Login-Wand einen Preis lesen.

```text
1. open        url="https://portal.example.com/login"
   → HTTP 200, Login-Formular geladen

2. snapshot    → Formularfelder sichtbar; Username-Feld @e3, Passwort @e7

3. fill        selector="@e3" value="demo-user"
4. fill        selector="@e7" value="••••••••"

5. click       selector="button[type=submit]"
6. wait        kind="url" value="/dashboard"

7. read        url="https://portal.example.com/dashboard"
   → Markdown im symfetch-Schema (Frontmatter: title, url, fetched_at,
     lang, tokens_est, schema_type)
```

Ein Agent, der `symfetch` kennt, kann `read` sofort lesen — dasselbe
Ausgabeschema, dieselben Feldnamen. `read --engine-hint` meldet zusätzlich,
ob JavaScript für den Inhalt nötig war (`js_required`), damit der Agent
beim nächsten Mal direkt den richtigen Tier wählt
([docs/tiers.md](tiers.md)).

## Verifikation

```sh
# Handshake ohne MCP-Client prüfen (muss zwei gültige JSON-RPC-Frames liefern):
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | ./symbrowse mcp
```

Der manuelle Handshake mit Claude Code, Cursor und OpenCode ist in PR #30
dokumentiert.

## SymFetch-Migration (fetch_url, fetch_batch, wayback_snapshots)

Seit der Repo-Konsolidierung (2026-08-23) ist die `symfetch`-Laufzeit
archiviert und ihre Static-Engine in `symbrowse` absorbiert. Damit Hermes
(und andere MCP-Clients) vom deaktivierten `symfetch`-Formula auf
`symbrowse` wechseln können, ohne die etablierten Fetch-Workflows zu
verlieren, exponiert der `symbrowse`-MCP-Server die drei SymFetch-Contracts
als First-Class-Tools (issue #258):

| SymFetch-Tool | symbrowse-Tool | Daemon-Frame | Ausgabe |
|---|---|---|---|
| `fetch_url` | `fetch_url` | `fetch.url` | Markdown (Default), JSON (`{url, final_url, title, lang, content, interactive}`) oder Text |
| `fetch_batch` | `fetch_batch` | `fetch.batch` | Array in Eingabereihenfolge, `{url, ok, content}` |
| `wayback_snapshots` | `wayback_snapshots` | `wayback.snapshots` | Array `{timestamp, url, status, mime_type, digest}` |

Jede `fetch_url`- und `fetch_batch`-Antwort trägt zusätzlich mehrere additive
Felder — die SymFetch-Feldnamen darüber bleiben unverändert:

- `meta` — `{status_code, final_url, lang, char_count, est_tokens, truncated,
  likely_client_rendered}`. Dieselben Feldnamen wie bei `read`
  ([output-schema.md](output-schema.md)). `truncated: true` heißt: Die Antwort
  ist gekürzt, es fehlt Inhalt.
- `escalate` — die Eskalations-Empfehlung, wenn eine statische Abholung den
  Inhalt nicht vollständig bekommen hat ([tiers.md](tiers.md)). Fehlt, wenn
  Tier 0 gereicht hat.
- `warnings` — bei `fetch_url` die Warnungen des HTML-Injection-Scans; bei
  `fetch_batch` liegen sie pro URL-Eintrag vor. Der Seiteninhalt wird dabei nie
  still verändert.
- `content_boundaries` — ein separates Boundary-Objekt für strukturierte
  Antworten; Markdown/Text umschließen den Seitenkörper mit den entsprechenden
  Nonce-Markern. Im MCP-Modus ist die Grenze standardmäßig aktiv; Frontmatter
  und Metadaten bleiben außerhalb.

`cache_get` liest einen `out_*`-Handle zurück und akzeptiert optional `range`
als 1-basierte Zeilenspanne. Das gilt sowohl für das Token-Budget als auch für
`store_full_text`; der MCP-Hint nennt deshalb dieses Tool statt eines Shell-
Kommandos. `symbrowse cache list` und `symbrowse cache clear` verwalten zusätzlich
den separaten Fetch-Response-Cache.

## Ausgabegrenzen

`max_chars` (Default 20 000) ist eine **durchgesetzte** Obergrenze für die
gerenderte Ausgabe, nicht nur ein Hinweis: Überlanger Inhalt wird gekürzt,
mit Marker im Text und `meta.truncated: true`. Der Metadaten-Kopf des
Markdowns zählt gegen dasselbe Budget. Bei `format: json` greift das Budget
auf Dokumentebene, damit die Antwort gültiges JSON bleibt.

Im MCP-Modus gilt zusätzlich das Token-Budget (Default 4 000 Tokens,
`mcpDefaultMaxTokens`) wie für `snapshot` und `read` — pro Aufruf über
`max_tokens` übersteuerbar.

Alle drei laufen über die **Non-Browser-Fetch-Pipeline** (honest HTTP +
StaticEngine): Sie brauchen keine Chrome-Sitzung und keinen Daemon-Browser.
SSRF-Guard gilt wie für alle MCP-Tools (private/loopback sind deny, außer
`--allow-private`). Die Request-/Result-Schemas bleiben stabil, damit
Clients, die die SymFetch-Contracts nutzen, unverändert weiterarbeiten.

### Hermes-Umstieg

1. `symbrowse` installieren (Homebrew: `brew install danieljustus/tap/symbrowse`
   oder aus dem Repo bauen) und `symbrowse version` prüfen.
2. In `~/.hermes/config.yaml` den `mcp.servers.symfetch`-Block ersetzen:

   ```yaml
   mcp:
     servers:
       symfetch:                       # wird ersetzt durch:
         command: /opt/homebrew/bin/symfetch
         args: [mcp]
         enabled: true
   ```
   → 

   ```yaml
   mcp:
     servers:
       symbrowse:
         command: /opt/homebrew/bin/symbrowse
         args: [mcp]
         enabled: true
   ```

3. Hermes neu starten und verifizieren:

   ```sh
   # Tools-Liste muss fetch_url, fetch_batch und wayback_snapshots enthalten:
   printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | /opt/homebrew/bin/symbrowse mcp
   # Live-Smoke-Test:
   printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fetch_url","arguments":{"url":"https://example.com","format":"json"}}}' | /opt/homebrew/bin/symbrowse mcp
   ```

4. Erst nach erfolgreichem Smoke-Test `symfetch` deaktivieren:
   `brew uninstall symfetch` (das Formula ist bereits als „absorbed into
   symaira-browse" markiert und disabled).

### Rollback

Falls ein Workflow an `symbrowse`-Seite unerwartet bricht, den
`mcp.servers`-Block zurück auf `symfetch` stellen, Hermes neu starten und
`fetch_url` erneut gegen das alte Binary prüfen. Beide Server können im
Konfigurationsblock auch **parallel** aktiv bleiben (unterschiedliche
Server-Namen); Clients wählen pro Task den passenden. Das archivierte
`symfetch`-Formula bleibt im Homebrew-Tap bis zur endgültigen Entfernung
verfügbar.



## Engine-Auswahl (`--engine`)

`symbrowse mcp` wählt die Engine wie `symbrowse daemon`:

```sh
symbrowse mcp --engine safari-bidi
```

Persistent über `config.toml`:

```toml
engine = "safari-bidi"
```

Die Präzedenz ist `--engine` → `SYMBROWSE_ENGINE` → `config.toml` → `chrome`.
Ein unbekannter Name wird beim Start abgewiesen, statt still auf Chrome
zurückzufallen.

Der MCP-Server reicht die aufgelöste Engine als `--engine` an den Daemon
weiter, den er selbst startet. Damit überlebt eine konfigurierte Safari-Engine
auch den Daemon-Autostart und jeden Neustart. `daemon status --json` meldet die
laufende Engine im Feld `engine`, `config show` das konfigurierte Feld `engine`
mit seiner Quelle.

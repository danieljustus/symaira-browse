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
| `core` (Default) | `open`, `snapshot`, `click`, `fill`, `type`, `press`, `wait`, `read`, `get`, `find` | Alltag: Seiten öffnen, ansehen, bedienen, lesen |
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

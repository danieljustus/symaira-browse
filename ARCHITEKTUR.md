# Symaira Browse (`symbrowse`) — Idee & Architektur

Stand: 2026-07-30 · Status: **Bauplan, beschlossen für v0.1.0 → v1.0.0**
Vorbild/Benchmark: [vercel-labs/agent-browser](https://github.com/vercel-labs/agent-browser) (Rust, CDP-Daemon)
Geprüft gegen: `../AGENTS.md`, `../ECOSYSTEM.md`, `symaira-fetch`, `symaira-guard`,
`symaira-vault`, `symaira-brain`, `symaira-skills`, `symaira-corekit`, `symaira-hub`.

---

## 1. Der eine Satz

> **Symaira Browse ist der Browser, den ein Agent bedienen kann und ein Mensch
> jederzeit übernehmen darf — ohne dass die Sitzung abreißt.**

`symbrowse` steuert einen echten Chrome über das Chrome DevTools Protocol,
adressiert Seitenelemente über deterministische, LLM-freundliche Referenzen
(`@e7`), hält Sitzungen zwischen Aufrufen warm, und hat eine **explizite,
eingebaute Übergabestelle an den Menschen** (Login, 2FA, CAPTCHA, Freigabe von
riskanten Aktionen) statt sie als Fehlerfall zu behandeln.

---

## 2. Warum ein neues Repo (und nicht in `symaira-fetch`)

Die Entscheidung wurde vor Anlage des Repos getroffen und ist hier festgehalten,
damit sie nicht neu diskutiert wird:

| Achse | `symfetch` | `symbrowse` |
|---|---|---|
| Kernversprechen | „**ohne** JavaScript-Overhead" | „**mit** JavaScript und Interaktion" |
| Prozessmodell | Prozess pro Aufruf, zustandslos | Daemon + Client, langlebige Sitzung |
| Abhängigkeiten | CGO-frei, keine externen Binaries | braucht einen Chrome/Chromium auf dem Host |
| Kosten pro Abruf | ~100 ms, ~30 MB | ~1–3 s, ~300 MB |
| Threat-Model | SSRF-Guard, read-only | Domain-Allowlist, Aktions-Policy, Credentials, Downloads, Uploads |
| MCP-Oberfläche | eine Tool-Familie | 40+ Kommandos → braucht Tool-Profile |

Ein Repo, das beides bündelt, würde jedem `symfetch`-Nutzer einen Browser-Stack
aufzwingen und das schärfste Verkaufsargument von Fetch („kein JS, kein
Browser") verwässern. **Fetch bleibt Tier 0. Browse ist Tier 1.** Die beiden
Repos sind über einen Eskalationsvertrag verbunden (§6.1), nicht über Code.

`symaira-fetch/AGENTS.md` hält bereits fest: *„JS execution is a future milestone
via the `pipeline.Engine` interface."* — Genau diese Stelle füllt `symbrowse`
aus, als eigenständiges Werkzeug hinter einem Laufzeit-Vertrag.

---

## 3. Position im Ökosystem

```
                     ┌──────────────────────────────────────────┐
   Mensch  ─────────▶│  OOB-Kanal: Handoff · Approval · Watch   │
                     └───────────────────┬──────────────────────┘
                                         │
  AI-Harness ──MCP──▶ [symguard] ──▶ ┌───┴────────┐
  (Claude Code,       (call-time     │ symbrowse  │──CDP──▶ Chrome
   Codex, Cursor)      enforcement)  │  (Daemon)  │
                                     └───┬────┬───┘
                                         │    │
                       symvault ◀────────┘    └────────▶ symfetch
                   (Credentials, nie                    (Tier-0-Eskalation,
                    im Agenten-Kontext)                  Markdown-Schema)
```

### 3.1 Laufzeit-Kompositionen (nie Compile-Time-Import eines Geschwister-Repos)

| Tool | Rolle für Browse | ab |
|---|---|---|
| **symaira-corekit** | `configkit`, `logkit`, `exitcodes`, `fsutil`, `mcpserver`, `versionkit`, `updatecheck` — exakt gepinnt | v0.1 |
| **symaira-fetch** | Tier-0-Partner. Browse übernimmt Fetchs Ausgabeschema; Fetch verweist bei SPA-Skeleton auf Browse | v0.3 |
| **symaira-vault** | Credential-Quelle. `symbrowse auth login <name>` tippt ein Secret in die Seite, **ohne** dass der Wert je in Agenten-Kontext, Log oder Trace landet | v0.4 |
| **symaira-guard** | Call-Time-Enforcement. Browse klassifiziert Risiko und delegiert die Entscheidung, wenn Guard installiert ist | v0.5 |
| **symaira-skills** | SSOT für **Flows** (§5.6) — Flows sind Skills mit ausführbarem Rumpf | v0.6 |
| **symaira-brain** | Exposure-Policy. Brain entscheidet, *welche* Browse-Tools ein Profil überhaupt sieht (Tool-Profile §5.5) | v0.3 (nur kompatibel designen) |
| **symaira-hub** | GUI-Modul „Browse": Live-Viewport, Aktions-Journal, Approve/Deny. Kein eigenes Web-Dashboard | nach v1.0 |

Standalone-First gilt strikt: `symbrowse` muss ohne jedes andere Symaira-Tool
bauen, testen und laufen. Jede Kopplung ist Laufzeit-Erkennung mit Fallback.

### 3.2 Bewusst draußen

| Nicht gebaut | Warum |
|---|---|
| `chat`-REPL / AI-Gateway (agent-browser hat das) | Cores rufen keine LLMs. Der Harness *ist* der Agent. Ein Core, der selbst inferenziert, bricht das Ökosystem-Prinzip und die Kostenkontrolle |
| Web-Dashboard | GUI-Strategie: **eine** Hub-App, keine Per-Tool-Web-UIs (`../ECOSYSTEM.md` §9) |
| Eigener Credential-Tresor (`auth save --password`) | Das ist `symvault`. Ein zweiter Tresor wäre ein Ökosystem-Konstruktionsfehler — dieselbe Regel wie Brain↔Guard |
| Eigenes Plugin-Protokoll (stdio-Plugins) | Symaira löst Erweiterung durch Laufzeit-Erkennung von Geschwister-CLIs, nicht durch ein zweites Protokoll |
| React-Introspektion, Web Vitals, CPU-Profiler, Tracing | Frontend-Dev-Werkzeug, nicht Agenten-Browsing. Kein Symaira-Publikum |
| Cloud-Provider (Browserbase, Kernel, AgentCore) | Gehört, wenn überhaupt, in ein späteres `symaira-browse-pro`. Open-Core-Grenze |
| CAPTCHA-Lösung | Grundsätzlich nicht. Die richtige Antwort ist der Handoff (§5.4) |

---

## 4. Was wir von agent-browser übernehmen — und was wir besser machen

| # | agent-browser | Übernahme | Symaira-Delta |
|---|---|---|---|
| A1 | **Client-Daemon** mit Idle-Timeout | ✅ vollständig | Unix-Socket unter `$XDG_RUNTIME_DIR`, `0600`; Idle-Default 30 min statt 60 |
| A2 | **A11y-Snapshot + `@e1`-Refs** | ✅ das Herzstück | **Stabile Refs über Snapshots hinweg** + `snapshot --diff` (§5.2). agent-browser regeneriert Refs bei jeder DOM-Änderung → Agent muss neu snapshotten und zahlt jedes Mal voll |
| A3 | `--max-output <chars>` | ✅ Idee | **Semantisches Token-Budget** statt Zeichen-Abschnitt, mit truncate-and-store wie Fetch (§5.3) |
| A4 | Semantische Finder (`find role/text/label`) | ✅ vollständig | unverändert gut |
| A5 | `batch` mit JSON-Array | ✅ vollständig | zusätzlich `--dry-run` (Plan ohne Ausführung) für Freigabe durch den Menschen |
| A6 | Isolierte **Sessions**, `--restore`, State-Verschlüsselung | ✅ vollständig | Schlüssel aus `symvault` statt 64-Hex-Env-Var; Session-ID aus Git-Worktree (passt zum `parallel-repo-agents`-Muster) |
| A7 | `--content-boundaries` | ✅ und weiter | **Injection-Heuristik** mit `warnings[]` (§5.7): versteckter Text, agentengerichtete Imperative, `aria-label`-Mismatch |
| A8 | `--allowed-domains`, `--action-policy`, `--confirm-actions` | ✅ Idee | Risiko-**Klassifikation** bleibt in Browse, die **Entscheidung** delegiert an `symguard`, wenn vorhanden (§5.5) |
| A9 | MCP-**Tool-Profile** (`core|network|state|debug|…`) | ✅ vollständig | Kritisch wichtig — 40+ Tools sprengen jeden Kontext. Default `core` |
| A10 | Auth-Vault, `auth login` ohne Klartext an das LLM | ✅ Idee | Umsetzung über `symvault request_credential`; Browse sieht das Secret nie, nur der Child-Prozess/CDP-Input |
| A11 | Engine-Abstraktion (`chrome`, `lightpanda`) | ✅ Idee | `internal/engine`-Interface ab v0.1 sauber gezogen, zweite Engine ab v1.0 |
| A12 | `--headed`, `stream`, `dashboard` | ⚠️ ersetzt | Ersetzt durch den **OOB-Kanal** (§5.4): Handoff, Watch, Approval — ein Mechanismus statt drei |
| A13 | `a11y` (axe-core) | ✅ später | Echter Nutzen, kein anderes Symaira-Tool besetzt das. Milestone M6 |
| A14 | `skills`-Kommando | ✅ umgedeutet | Wird zu **Flows** (§5.6), gespeichert und gerendert von `symskills` |

---

## 5. Die sieben tragenden Ideen

### 5.1 Daemon, Session, Client

Ein Aufruf `symbrowse click @e7` darf keine 2 s Chrome-Start kosten. Also:

- **Client** (`symbrowse …`) ist ein dünner Prozess: parst, sendet JSON-Frame an
  den Socket, druckt Antwort, endet. Exit-Codes aus `corekit/exitcodes`.
- **Daemon** (`symbrowse daemon`, autostart) hält pro **Session** genau einen
  Browser-Kontext: Chrome-Prozess, Tabs, Ref-Tabelle, Cookies, Journal.
- **Socket**: `$XDG_RUNTIME_DIR/symbrowse/<session>.sock` (macOS:
  `~/Library/Caches/symbrowse/run/`), Modus `0600`, Peer-UID-Prüfung.
- **Idle-Shutdown** nach 30 min (`SYMBROWSE_IDLE_TIMEOUT`), außer bei
  `--headed` oder aktivem Handoff.
- **Timeouts**: Operation 25 s, IPC-Read 30 s — bewusst gestaffelt, damit ein
  Operations-Timeout eine saubere Fehlermeldung liefert statt eines
  abgerissenen Sockets. (Direkt von agent-browser übernommen; die Staffelung ist
  richtig durchdacht.)

Der CDP-Zugriff liegt hinter `internal/engine.Engine`. Implementierung v0.1:
`cdproto` (die generierten Protokoll-Bindings aus dem chromedp-Projekt) über
eine eigene, schlanke WebSocket-Session — analog zur Isolationsregel in Fetch
(*„swap the implementation in `internal/fetch/azuretls.go` only"*). Kein
CGO. Chrome wird **nicht** gebündelt, sondern gefunden (`symbrowse doctor`).

### 5.2 Stabile Refs — der wichtigste technische Unterschied

agent-browser: *„Refs are stable within a snapshot but regenerated after page
navigation or DOM changes."* Das ist die teuerste Eigenschaft für einen Agenten:
Nach jedem Klick ist die gesamte Adressierung ungültig, also folgt auf jede
Aktion ein voller Snapshot (oft 3–10k Token).

Symaira Browse leitet die Ref aus einem **inhaltsadressierten Schlüssel** ab:

```
refkey = sha256( role ‖ accessible-name ‖ normalisierter DOM-Pfad ‖ Geschwister-Ordinal )
```

Der Daemon hält `refkey → @eN` pro Session. Beim Re-Snapshot:

- gleicher `refkey` → **gleiche Nummer** (`@e7` bleibt `@e7`)
- neuer `refkey` → nächste freie Nummer
- verschwundener `refkey` → **Tombstone**. Ein Zugriff auf `@e7` liefert nicht
  „not found", sondern `{"error":"stale_ref","was":"button \"Speichern\"","reason":"removed_after_navigation","hint":"snapshot --diff"}`

Dazu `symbrowse snapshot --diff`: gibt nur `+ / - / ~`-Zeilen gegenüber dem
letzten Snapshot derselben Session aus. Typischer Klick-Folgesnapshot schrumpft
damit von tausenden Token auf einige Dutzend.

**Wirkung:** Der Agent kann einen Plan über mehrere Schritte formulieren
(„fülle @e3, @e4, dann klick @e9") statt nach jeder Aktion neu zu sehen.

### 5.3 Token-Budget statt Zeichen-Abschnitt

Jede ausgabestarke Operation (`snapshot`, `read`, `get html`, `console`,
`network requests`) akzeptiert `--max-tokens N`. Bei Überschreitung wird
**nicht** hart abgeschnitten, sondern das Hermes-Muster aus `symfetch`
angewandt: Kopf + Fuß zurückgeben, Vollinhalt im Cache ablegen, Handle
mitliefern.

```json
{
  "truncated": true,
  "tokens_returned": 1200,
  "tokens_total": 18400,
  "cache_id": "snap_9f2c",
  "hint": "symbrowse cache get snap_9f2c --range 40-120"
}
```

Defaults im MCP-Modus deutlich strenger als im TTY-Modus (Mensch scrollt,
Agent zahlt). `snapshot` ist im MCP-Modus per Default `--interactive --compact`.

### 5.4 Der OOB-Kanal: Mensch und Agent an derselben Sitzung

Das ist die Antwort auf die eigentliche Frage („optimale Zusammenarbeit
Mensch ↔ AI-Agent") und der Punkt, an dem Symaira Browse sich von jedem
Konkurrenten unterscheidet.

Drei Situationen, ein Mechanismus:

**(a) Handoff — der Agent kommt nicht weiter.**
Login-Wand, 2FA-Code, CAPTCHA, Zahlungsbestätigung, „Ich bin kein Roboter".

```bash
symbrowse handoff --reason "2FA-Code aus der Authenticator-App nötig" --timeout 5m
```

Der Daemon schaltet die Sitzung sichtbar (Zustand wird gesichert, Chrome headed
neu gestartet, Zustand restauriert), blendet ein Overlay-Banner in die Seite
(„Symaira Browse: der Agent wartet — Grund: …" + Button *Fertig* / *Abbrechen*),
schickt eine macOS-Notification, und blockiert den Aufruf. Rückgabe:

```json
{"status":"completed","duration_ms":41200,"url_after":"https://…/dashboard","snapshot_diff":"…"}
```

Der Agent macht weiter, als wäre nichts gewesen — **mit derselben Session, denselben
Cookies, denselben Refs**. Kein Neustart, kein „bitte logge dich vorher manuell ein".

**(b) Approval — der Agent will etwas Riskantes.**
Im MCP-Modus ist stdin der JSON-RPC-Kanal, ein TTY-Prompt ist unmöglich.
agent-browser verweigert dort schlicht (`auto-denies if stdin is not a TTY`).
Symaira nutzt denselben OOB-Kanal: die Freigabefrage erscheint als
Overlay/Notification, nicht auf stdin. Damit funktioniert Bestätigung genau
dort, wo sie am nötigsten ist.

**(c) Watch — der Mensch schaut zu.**
`symbrowse watch` spiegelt die laufende Agentensitzung headed und read-only,
plus Live-Aktions-Journal im Terminal. Vertrauen entsteht durch Beobachtbarkeit,
nicht durch Versprechen. (Hub-Modul später auf derselben JSON-Schnittstelle.)

**Journal & Replay.** Jede Aktion wird append-only protokolliert:
Zeitstempel, Kommando, Ziel-Refkey, URL vorher/nachher, Risiko-Klasse,
Entscheider (`policy` / `human` / `guard`), Ergebnis. `symbrowse trace export`
erzeugt daraus eine **deterministisch wiederholbare** Datei — der Mensch kann
prüfen, was passiert ist, und es exakt nachfahren. Das Format ist bewusst
kompatibel zu dem, was `symaira-room` als signiertes Journal plant, und zu
Guards Hash-Chain-Audit.

### 5.5 Risiko-Klassen, Policy und die Guard-Grenze

Jedes Kommando trägt fest verdrahtet eine **Risiko-Klasse**:

| Klasse | Beispiele | Default MCP | Default TTY |
|---|---|---|---|
| `read` | `snapshot`, `get`, `read`, `screenshot` | allow | allow |
| `navigate` | `open`, `back`, `reload` | allow (Allowlist) | allow |
| `interact` | `click`, `fill`, `hover`, `select` | allow (Allowlist) | allow |
| `submit` | Klick auf `type=submit`, `press Enter` in Form | **confirm** | allow |
| `eval` | `eval`, `wait --fn` | **confirm** | confirm |
| `credential` | `auth login` | **confirm** | confirm |
| `download` / `upload` | `upload`, Datei-Downloads | **confirm** | confirm |
| `network-mock` | `network route --body` | **deny** | confirm |

**Die Grenze zu `symguard` ist die wichtigste Architekturentscheidung dieses
Dokuments** — dieselbe Logik wie bei Brain↔Guard:

- **Browse klassifiziert** (es allein weiß, dass dieser Klick ein Formular
  absendet und diese Domain nicht auf der Allowlist steht).
- **Guard entscheidet**, wenn installiert und konfiguriert: Browse ruft
  `symguard` als lokalen Entscheider auf und respektiert das Urteil.
- Ohne Guard greift eine **minimale eingebaute Policy** (Tabelle oben +
  `policy.toml`). Kein zweiter Policy-Motor, keine Risiko-Ontologie-Duplikate.

Domain-Allowlist ist zusätzlich hart im Netzwerk-Layer: Navigation,
Subresources, WebSocket, EventSource, `sendBeacon`, Worker. Plus SSRF-Guard
(RFC1918/Loopback) mit denselben Defaults wie `symfetch`.

### 5.6 Flows — der Weg aus der Token-Falle

Ein Agent, der zum zwanzigsten Mal denselben Login durchklickt, verbrennt jedes
Mal denselben Kontext. Ein **Flow** ist ein deklarativer, versionierter,
menschlich prüfbarer Ablauf:

```yaml
name: jira-ticket-anlegen
version: 1
domains: ["jira.example.com"]
inputs: [title, description]
steps:
  - open: "https://jira.example.com/browse/NEW"
  - find: { label: "Summary", action: fill, value: "{{title}}" }
  - find: { label: "Description", action: fill, value: "{{description}}" }
  - assert: { visible: "button[name=Create]" }
  - find: { role: button, name: "Create", action: click }
  - wait: { url: "**/browse/*" }
outputs:
  ticket_url: { from: url }
```

- `symbrowse flow record` zeichnet eine erfolgreiche Agentensitzung auf und
  **generiert** einen Flow-Entwurf (inkl. Assertions aus dem beobachteten Zustand).
- Der Mensch prüft den Diff und merged ihn — Code-Review für Agentenverhalten.
- `symbrowse flow run jira-ticket-anlegen --input title=…` ist danach ein
  **einziger** Tool-Call statt zwanzig, deterministisch und auditierbar.
- Secrets in Flows nur als Referenz (`op://…`), nie als Wert.
- Gespeichert/verteilt über `symskills` (SSOT), damit Flows dieselbe
  Discovery bekommen wie Skills.

Das ist die eigentliche Mensch-Agent-Arbeitsteilung: **Der Agent exploriert
einmal, der Mensch segnet ab, danach läuft es billig und reproduzierbar.**

### 5.7 Seiteninhalt ist feindliches Eingabematerial

Jede zurückgegebene Seitenausgabe wird in Boundary-Marker gefasst
(`--content-boundaries`, im MCP-Modus Default an) **und** heuristisch geprüft:

- **Versteckter Text**: `display:none`, `visibility:hidden`, `font-size:0`,
  `opacity:0`, Off-Viewport, Vordergrund≈Hintergrund
- **Agentengerichtete Imperative** (mehrsprachig): „ignore previous
  instructions", „du bist ein KI-Assistent", „system prompt", „rufe folgendes
  Tool auf", „API key"
- **`aria-label`-Mismatch**: sichtbarer Text ≠ zugänglicher Name auf einem
  interaktiven Element (klassischer Klick-Umleitungsangriff)
- **Instruktionen in `alt`, `title`, HTML-Kommentaren, `<meta>`**

Ergebnis als `warnings[{kind, severity, ref, excerpt}]`. **Nicht stillschweigend
entfernen** — das versteckt den Angriff. Melden, markieren, und die Policy
entscheiden lassen (z. B. `submit` erzwingt Approval, wenn die Seite
`severity: high` trägt).

---

## 6. Verträge nach außen

### 6.1 Der Eskalationsvertrag mit `symfetch`

Ein Agent soll nicht raten müssen, welches der beiden Werkzeuge er braucht.

1. `symfetch` erkennt bereits SPA-Skeletons und dünnen Inhalt. Es ergänzt in
   diesem Fall ein Feld:
   `{"escalate": {"tool":"symbrowse","reason":"spa_skeleton","command":"symbrowse read <url>"}}`
   (Upstream-Issue in `symaira-fetch`, siehe PLANUNG.md §Cross-Repo.)
2. `symbrowse read <url>` liefert **dasselbe Ausgabeschema** wie `symfetch`:
   YAML-Frontmatter (`title`, `url`, `fetched_at`, `lang`, `schema_type`),
   Markdown-Körper, dieselben JSON-Feldnamen, dieselbe Truncate-and-store-Semantik.
   Ein Agent, der Fetch kennt, kann Browse sofort lesen.
3. `symbrowse read --engine-hint` meldet zurück, ob JS überhaupt nötig war —
   damit ein Agent beim nächsten Mal direkt Tier 0 nehmen kann.

**Code-Teilung:** v0.1 bis v0.5 implementiert Browse einen eigenen, minimalen
HTML→Markdown-Pfad. Die Extraktion der semantischen DOM-Pipeline aus Fetch nach
`corekit` (Arbeitstitel `domkit`) erfolgt erst in M6 — nach der
Ökosystem-Regel: *erst im Tool implementieren, bei bewiesenem zweitem
Konsumenten extrahieren.* Dann ist Browse dieser zweite Konsument und die
Schnittstelle ist aus zwei realen Nutzungen abgeleitet statt geraten.

### 6.2 MCP-Oberfläche

`symbrowse mcp [--tools core|nav|state|network|debug|flows|all]`, Default `core`.

| Profil | Enthält |
|---|---|
| `core` | `open`, `snapshot`, `click`, `fill`, `type`, `press`, `wait`, `read`, `get`, `screenshot`, `close`, `find` |
| `nav` | `back`, `forward`, `reload`, Tabs, Frames, Dialoge |
| `state` | Cookies, Storage, Sessions, `state save/load`, `auth login` |
| `network` | Routing, Mocking, Request-Inspektion, HAR, Header, Offline |
| `debug` | Console, Errors, `eval`, `a11y`, `diff`, `doctor` |
| `flows` | `flow list/run/record` |
| `all` | volle CLI-Parität |

Jedes Tool nimmt `session` entgegen. Handshake über `corekit/versionkit`
(`{tool, version, schema_version}`), damit Hub und Brain sauber andocken.
Kein `os.Stdout` außerhalb von JSON-RPC-Frames — Logs ausschließlich
über `logkit` nach stderr (Ökosystem-Regel „Zero Stdio Pollution").

### 6.3 Konfiguration

Über `corekit/configkit`, Priorität (niedrig → hoch):

1. `~/.config/symbrowse/config.toml`
2. `./.symbrowse.toml` (Projekt-Override)
3. `SYMBROWSE_*` Umgebungsvariablen
4. CLI-Flags

XDG-Pfade: Config `~/.config/symbrowse`, Cache `~/.cache/symbrowse`,
State `~/.local/state/symbrowse` (Sessions, Journal, Flows-Cache).
**TOML, nicht JSON** — Ökosystem-Konvention (Fetch/Memory/Fritz/Print).

---

## 7. Repository-Struktur (Ziel)

```
symaira-browse/
├── cmd/symbrowse/            # Cobra-Entrypoint, Client + `daemon`-Subcommand
├── internal/
│   ├── engine/               # Engine-Interface + chrome/ (CDP über cdproto)
│   ├── daemon/               # Socket-Server, Session-Registry, Idle-Shutdown
│   ├── session/              # Browser-Kontext, Tabs, Lebenszyklus
│   ├── refs/                 # Refkey-Berechnung, Ref-Tabelle, Tombstones, Diff
│   ├── snapshot/             # A11y-Baum → LLM-Text, Filter, Kompaktierung
│   ├── render/               # HTML → Markdown/JSON, Fetch-kompatibles Schema
│   ├── budget/               # Token-Zählung, truncate-and-store, Cache
│   ├── policy/               # Risiko-Klassen, Allowlist, lokale Policy
│   ├── guard/                # optionale symguard-Delegation (Laufzeit-Erkennung)
│   ├── oob/                  # Handoff, Approval, Watch, Overlay, Notification
│   ├── journal/              # Append-only Aktions-Log, Trace-Export/Replay
│   ├── credential/           # symvault-Anbindung, Klartext-freie Eingabe
│   ├── injection/            # Boundary-Marker + Heuristiken
│   ├── flows/                # Flow-Parser, Runner, Recorder
│   ├── state/                # Cookies/Storage speichern/laden, Verschlüsselung
│   ├── mcp/                  # MCP-Server auf corekit/mcpserver, Tool-Profile
│   └── config/               # configkit-Wiring
├── docs/
├── ARCHITEKTUR.md
├── PLANUNG.md
├── AGENTS.md
└── README.md
```

Go 1.26.5 (Core-Tier), `CGO_ENABLED=0`, Apache-2.0, Binary `symbrowse`,
Env-Präfix `SYMBROWSE_`, GoReleaser + Cosign + Syft + Homebrew-Tap wie die
übrigen Cores.

---

## 8. Nicht-Ziele für v1.0

- Kein Firefox/WebKit (CDP-only; die Engine-Abstraktion hält die Tür offen)
- Kein Mobile-Device-Farming, kein iOS/Safari-Provider
- Kein verteiltes Crawling, keine Frontier, keine Queue (das wäre `symseek`/`symingest`-Terrain)
- Keine Bot-Detection-Umgehung über TLS-Fingerprinting (das kann `symfetch`; Browse ist ein echter Browser und braucht es nicht)
- Keine CAPTCHA-Automatisierung
- Kein eigenes GUI

---

## 9. Risiken und wie wir sie adressieren

| Risiko | Adressierung |
|---|---|
| **Chrome-Abhängigkeit** bricht die „ein Binary"-Erfahrung | `symbrowse doctor` findet/prüft Chrome, `doctor --fix` gibt exakte Installationsanweisung; klare Fehlermeldung statt Absturz |
| **CDP-Drift** bei Chrome-Updates | Protokoll-Bindings gepinnt, Engine-Interface isoliert Änderungen auf ein Paket (Fetch-Muster mit azuretls) |
| **Ref-Stabilität** funktioniert bei stark dynamischen SPAs schlechter als versprochen | Refkey enthält bewusst mehrere Merkmale; Tombstone-Fehler nennt immer den Grund; `--diff` bleibt korrekt auch bei vollständiger Neuvergabe. Messkriterium in M1: ≥ 80 % Ref-Erhalt über einen Klick auf einer Referenz-Testseite |
| **Daemon als Sicherheitsfläche** (jeder lokale Prozess könnte den Browser steuern) | Socket `0600`, Peer-UID-Prüfung, kein TCP-Default, Token für optionalen HTTP-Modus |
| **Prompt-Injection** über Seiteninhalt | §5.7 — Marker + Heuristik + Policy-Kopplung. Explizit ein v0.2-Thema, kein Nachgedanke |
| **Feature-Explosion** (agent-browser hat 200+ Kommandos) | §3.2 Nicht-Ziele sind bindend. Jede Milestone hat ein „nicht in dieser Stufe"-Feld |
| **Sitzungs-Leck zwischen parallelen Agenten** | Session-ID aus Git-Worktree, getrennte Profil-Verzeichnisse, `session list` zeigt Besitzer |

---

## 10. Entscheidungsprotokoll

| # | Entscheidung | Begründung |
|---|---|---|
| E1 | Eigenes Repo `symaira-browse`, Binary `symbrowse` | §2 |
| E2 | Go 1.26.5, CGO-frei, corekit-basiert | Ökosystem-Konsistenz; Rust wäre schneller, aber isoliert das Repo vom gesamten Werkzeugkasten |
| E3 | CDP direkt über `cdproto` hinter `internal/engine` | Volle Protokollkontrolle (A11y-Baum, Emulation, Network) ohne High-Level-Framework-Diktat; austauschbar |
| E4 | Kein eigener Credential-Tresor | `symvault` besetzt die Stelle. Zweiter Tresor = Konstruktionsfehler |
| E5 | Risiko-Klassifikation in Browse, Entscheidung in Guard | Analog Brain↔Guard. Zwei Policy-Motoren an derselben Kante wären ein Ökosystem-Fehler |
| E6 | OOB-Kanal vereint Handoff, Approval und Watch | Drei Bedürfnisse, ein Mechanismus. Löst zugleich das „kein TTY im MCP-Modus"-Problem, an dem agent-browser scheitert |
| E7 | Stabile Refs + `--diff` als Kernfeature, nicht als Optimierung | Größter Token-Hebel und größter Unterschied zum Vorbild |
| E8 | Flows über `symskills`, nicht als eigenes Registry | SSOT-Regel |
| E9 | `domkit`-Extraktion nach corekit erst in M6 | „Erst implementieren, bei zweitem Konsumenten extrahieren" |
| E10 | Kein `chat`, kein Dashboard, kein Plugin-Protokoll | §3.2 |
| E11 | TOML-Config, XDG-Pfade, `SYMBROWSE_*` | Ökosystem-Konvention |
| E12 | Ausgabeschema identisch zu `symfetch` | Ein Agent lernt ein Schema, nicht zwei |

---

## 11. Erfolgskriterien für v1.0

1. Ein Agent kann einen Login mit 2FA abschließen, indem er **einmal** den
   Menschen um Übernahme bittet — ohne Sitzungsverlust.
2. Ein Klick-Folgesnapshot kostet im Median **< 200 Token** (`--diff`).
3. `symbrowse read` und `symfetch` liefern für dieselbe Seite strukturell
   identisches Markdown/JSON.
4. Ein aufgezeichneter Flow ersetzt eine 20-Schritt-Sequenz durch **einen**
   Tool-Call und läuft ohne Modellzugriff.
5. Kein Secret erscheint jemals in Agenten-Kontext, Log, Journal oder Trace.
6. `symbrowse mcp` mit Default-Profil belegt **< 15** Tools im Kontext.
7. Jede Agentensitzung ist als Trace exportierbar und deterministisch nachfahrbar.

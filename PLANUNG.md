# Symaira Browse — Umsetzungsplan für GitHub (Issues & Milestones)

Stand: 2026-07-30 · Repo: `danieljustus/symaira-browse` · Basis: [ARCHITEKTUR.md](ARCHITEKTUR.md)

---

## 0. Auftrag an den ausführenden Agenten

Dieses Dokument ist **maschinennah gemeint**: Es enthält alles, was nötig ist, um
Labels, Milestones und Issues in GitHub anzulegen, ohne weitere Rückfragen.

**Reihenfolge:**

1. Labels anlegen (§1) — idempotent, vorhandene überspringen.
2. Milestones anlegen (§2) — Titel exakt übernehmen, Beschreibung übernehmen.
3. Issues anlegen (§3) in aufsteigender B-Nummer. Der **Titel** ist die
   `titel:`-Zeile **ohne** das `B-xx`-Präfix; die B-Nummer gehört als erste
   Zeile in den Body (`Plan-ID: B-xx`), damit Querverweise auflösbar bleiben.
4. Abhängigkeiten aus `nach:` als Body-Zeile
   `Abhängig von: #<issue-nummer>` nachtragen, sobald die referenzierten Issues
   Nummern haben (zweiter Durchlauf).
5. Cross-Repo-Issues (§4) in den dort genannten **fremden** Repos anlegen.
6. Alle Issues dem Projekt zuordnen, falls ein Projektboard existiert;
   sonst diesen Schritt überspringen und im Abschlussbericht vermerken.

**Body-Vorlage pro Issue:**

```
Plan-ID: B-xx
Quelle: ARCHITEKTUR.md §<abschnitt>

## Kontext
<kontext>

## Aufgabe
<aufgabe>

## Akzeptanzkriterien
- [ ] …

## Nicht in diesem Issue
<abgrenzung, falls angegeben>
```

**Regeln, die für jedes Issue gelten** (nicht in jedes Issue kopieren, einmal in
`AGENTS.md` des Repos festhalten):

- Go 1.26.5, `CGO_ENABLED=0`, Apache-2.0.
- Kein `os.Stdout` außerhalb von JSON-RPC-Frames; Logs via `logkit` → stderr.
- Standalone-First: kein Compile-Time-Import eines Geschwister-Repos.
- Jede neue Ausgabe hat einen JSON-Modus und ein stabiles Feldschema.
- Jede neue Aktion bekommt eine Risiko-Klasse (ab M4 erzwungen, davor
  im Code als Konstante vorbereitet).

---

## 1. Labels

| Label | Farbe | Beschreibung |
|---|---|---|
| `area:engine` | `1d76db` | CDP, Chrome-Anbindung, Engine-Interface |
| `area:daemon` | `1d76db` | Daemon, Socket, Session-Lebenszyklus |
| `area:refs` | `0e8a16` | Snapshot, Ref-Stabilität, Diff |
| `area:output` | `0e8a16` | Markdown/JSON-Rendering, Token-Budget |
| `area:mcp` | `5319e7` | MCP-Server, Tool-Profile |
| `area:security` | `b60205` | Policy, Allowlist, Injection, SSRF |
| `area:oob` | `d93f0b` | Handoff, Approval, Watch, Journal |
| `area:flows` | `fbca04` | Flow-Schema, Runner, Recorder |
| `area:state` | `c2e0c6` | Sessions, Cookies, Storage, Credentials |
| `area:ci` | `bfd4f2` | Build, Test, Release, Distribution |
| `area:docs` | `bfd4f2` | Dokumentation |
| `type:feature` | `a2eeef` | Neue Funktion |
| `type:chore` | `ededed` | Infrastruktur, Wartung |
| `type:test` | `d4c5f9` | Tests, Fixtures, Benchmarks |
| `prio:high` | `b60205` | Blockiert die Milestone |
| `prio:normal` | `ededed` | Standard |
| `cross-repo` | `006b75` | Betrifft ein anderes Symaira-Repo |

---

## 2. Milestones

| Titel | Beschreibung | Ergebnis |
|---|---|---|
| **v0.1.0 — Fundament** | Daemon, CDP-Engine, Sessions, Snapshot mit Refs, Grundinteraktion, CI/Release. | Ein Agent kann eine Seite öffnen, sehen, klicken und ausfüllen. |
| **v0.2.0 — Agenten-Ergonomie** | Stabile Refs, Snapshot-Diff, Token-Budget, `read` im Fetch-Schema, semantische Finder, Batch, Injection-Warnungen. | Ein Klick-Folgesnapshot kostet < 200 Token im Median. |
| **v0.3.0 — MCP** | MCP-Server mit Tool-Profilen, Allowlist, SSRF-Guard, Eskalationsvertrag mit `symfetch`. | Als MCP-Server in Claude Code/Cursor/OpenCode nutzbar, Default-Profil < 15 Tools. |
| **v0.4.0 — Sessions, Zustand, Credentials** | Cookies/Storage, State-Persistenz + Verschlüsselung, Restore, Worktree-Scoping, `symvault`-Anbindung. | Ein Login überlebt Neustarts; kein Secret erreicht den Agenten-Kontext. |
| **v0.5.0 — Mensch ↔ Agent** | OOB-Kanal (Handoff, Approval, Watch), Aktions-Journal, Trace-Replay, `symguard`-Delegation. | Ein Agent schließt einen 2FA-Login per Handoff ab, ohne die Sitzung zu verlieren. |
| **v0.6.0 — Flows** | Flow-Schema, Runner, Recorder, `symskills`-Anbindung. | Eine 20-Schritt-Sequenz wird zu einem deterministischen Tool-Call. |
| **v1.0.0 — Reichweite & Härtung** | Tabs/Frames/Dialoge, Netzwerk/HAR, `eval`, a11y-Audit, Diff, zweite Engine, `domkit`-Extraktion, Self-Update, Doku. | Feature-Parität mit dem Vorbild in allen Bereichen, die zum Symaira-Auftrag gehören. |

---

## 3. Issues

### Milestone v0.1.0 — Fundament

---
**B-01** · `titel:` Repo-Scaffolding: Go-Modul, Cobra-Entrypoint, Makefile
`labels:` area:ci, type:chore, prio:high · `milestone:` v0.1.0
**Kontext:** Das Repo enthält nur LICENSE, ARCHITEKTUR.md und PLANUNG.md.
**Aufgabe:** Go-Modul `github.com/danieljustus/symaira-browse` (Go 1.26.5),
`cmd/symbrowse/main.go` mit Cobra, `Makefile` (`build`, `test`, `test-race`,
`lint`, `fmt-check`, `clean`), `README.md`, `AGENTS.md`, `CLAUDE.md` (Zeiger auf
AGENTS.md), `.gitignore`, `.editorconfig`, Community-Dateien
(`CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`).
**Akzeptanzkriterien:**
- [ ] `make build` erzeugt `symbrowse`, `CGO_ENABLED=0` gesetzt
- [ ] `symbrowse --help` und `symbrowse version` funktionieren
- [ ] `make lint` und `make test` laufen grün (auch ohne Tests)
- [ ] `AGENTS.md` enthält die Repo-Regeln aus PLANUNG.md §0

---
**B-02** · `titel:` corekit-Wiring: Config, Logging, Exit-Codes, XDG-Pfade
`labels:` area:ci, type:chore, prio:high · `milestone:` v0.1.0 · `nach:` B-01
**Aufgabe:** `symaira-corekit` exakt pinnen und `configkit`, `logkit`,
`exitcodes`, `versionkit` verdrahten. TOML-Config mit Priorität
`~/.config/symbrowse/config.toml` < `./.symbrowse.toml` < `SYMBROWSE_*` < Flags.
XDG-Pfade für Config/Cache/State.
**Akzeptanzkriterien:**
- [ ] `symbrowse config show` gibt die effektive Konfiguration inkl. Herkunft je Feld aus
- [ ] `SYMBROWSE_LOG_LEVEL=debug` schreibt strukturierte Logs **nach stderr**
- [ ] Kein Paket schreibt nach `os.Stdout` außer dem Ausgabe-Layer
- [ ] Test: Prioritätskette über alle vier Ebenen

---
**B-03** · `titel:` CI-Workflow nach Ökosystem-Standard (PR-fast / main-comprehensive)
`labels:` area:ci, type:chore, prio:high · `milestone:` v0.1.0 · `nach:` B-01
**Aufgabe:** `.github/workflows/ci.yml` mit `paths-ignore` (docs/md/LICENSE),
`concurrency: cancel-in-progress`. PR-Gate: lint + ubuntu-Tests, Ziel < 5 min.
Volle Matrix (ubuntu/macOS/windows) nur auf `main`-Push und wöchentlich.
CodeQL schedule-only. Dependabot. Branch-Protection-Hinweis in der Issue-Beschreibung.
**Akzeptanzkriterien:**
- [ ] PR-Lauf < 5 min auf leerem Repo
- [ ] Docs-only-Commit löst keinen CI-Lauf aus
- [ ] Required Checks referenzieren ausschließlich PR-Jobs

---
**B-04** · `titel:` Chrome-Erkennung und `symbrowse doctor`
`labels:` area:engine, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-02
**Kontext:** Browse bündelt keinen Browser. Fehlender Chrome ist der häufigste
Erstnutzungsfehler und darf nie als Absturz erscheinen.
**Aufgabe:** Suchpfade für Chrome/Chromium/Edge auf macOS, Linux, Windows;
Versionsermittlung; `SYMBROWSE_EXECUTABLE_PATH`-Override. `symbrowse doctor`
prüft Chrome, CDP-Erreichbarkeit, Schreibrechte auf XDG-Pfaden, Socket-Verzeichnis.
`doctor --fix` gibt konkrete, kopierbare Anweisungen aus (installiert nichts selbst).
**Akzeptanzkriterien:**
- [ ] `doctor` und `doctor --json` mit Status je Prüfung
- [ ] Fehlender Chrome → Exit-Code aus `exitcodes`, Meldung nennt Suchpfade und Override-Variable
- [ ] Getestet mit manipuliertem PATH

---
**B-05** · `titel:` Engine-Interface + Chrome-Implementierung über CDP
`labels:` area:engine, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-04
**Aufgabe:** `internal/engine` mit `Engine`-Interface (Launch, NewContext,
NewPage, Navigate, Evaluate, AXTree, Screenshot, Close) und Implementierung
`internal/engine/chrome`: Chrome-Start mit eigenem User-Data-Dir, WebSocket auf
den DevTools-Endpoint, Domains `Target`, `Page`, `Runtime`, `DOM`,
`Accessibility`, `Network`, `Emulation`, `Input`. Protokoll-Bindings gepinnt.
**Akzeptanzkriterien:**
- [ ] Sämtlicher CDP-Zugriff liegt unter `internal/engine`; kein anderes Paket importiert Protokoll-Typen
- [ ] Sauberer Shutdown lässt keinen verwaisten Chrome-Prozess zurück (Test)
- [ ] Chrome-Start mit `--no-first-run`, ohne Default-Profil, ohne Telemetrie-Flags
**Nicht in diesem Issue:** zweite Engine (→ B-60)

---
**B-06** · `titel:` Daemon: Unix-Socket, Frame-Protokoll, Autostart, Idle-Shutdown
`labels:` area:daemon, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-05
**Aufgabe:** `symbrowse daemon` als Server auf
`$XDG_RUNTIME_DIR/symbrowse/<session>.sock` (macOS: `~/Library/Caches/symbrowse/run/`),
Modus `0600`, Peer-UID-Prüfung. Newline-getrenntes JSON-Frame-Protokoll
(`{cmd, args, session, request_id}` → `{success, data|error, warnings}`).
Client startet den Daemon bei Bedarf automatisch. Idle-Shutdown nach 30 min
(`SYMBROWSE_IDLE_TIMEOUT`, `0` = aus). Operation-Timeout 25 s, IPC-Read 30 s.
**Akzeptanzkriterien:**
- [ ] Zweiter Aufruf desselben Kommandos ist messbar schneller als der erste (Test)
- [ ] Socket ist für andere UIDs nicht nutzbar (Test)
- [ ] Operation-Timeout liefert strukturierten Fehler, keinen Socket-Abbruch
- [ ] `symbrowse daemon stop` und `daemon status`

---
**B-07** · `titel:` Session-Registry und Isolation
`labels:` area:daemon, area:state, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-06
**Aufgabe:** `--session <name>` (Default `default`), je Session eigener
Browser-Kontext, eigenes User-Data-Dir, eigene Ref-Tabelle. `session list`,
`session info --json` (PID, Startzeit, aktive Tabs, letzte Aktivität).
**Akzeptanzkriterien:**
- [ ] Zwei Sessions teilen keine Cookies (Test gegen lokalen Testserver)
- [ ] `session list --json` mit stabilem Schema
- [ ] Sessions überleben Client-Aufrufe, nicht aber Idle-Shutdown

---
**B-08** · `titel:` Navigation und Warten
`labels:` area:engine, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-07
**Aufgabe:** `open|goto <url>`, `back`, `forward`, `reload`. `wait <selector>`,
`wait <ms>`, `wait --text`, `wait --url <glob>`, `wait --load load|domcontentloaded|networkidle`,
`wait <sel> --state visible|hidden|attached|detached`.
**Akzeptanzkriterien:**
- [ ] Alle Wait-Varianten mit Timeout-Fehler, der nennt, worauf gewartet wurde und was stattdessen vorlag
- [ ] Navigation gibt Endzustand zurück (finale URL nach Redirects, HTTP-Status)
- [ ] Tests gegen Fixtures aus B-15

---
**B-09** · `titel:` A11y-Snapshot: Baum, Filter, kompakte LLM-Darstellung
`labels:` area:refs, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-08
**Aufgabe:** `Accessibility.getFullAXTree` → Textdarstellung im Stil
`- button "Speichern" [ref=e2]`. Flags `-i/--interactive`, `-c/--compact`
(leere Strukturknoten entfernen), `-d/--depth <n>`, `-s/--selector <sel>`,
`-u/--urls` (href bei Links). `--json` mit Baum + Ref-Map.
**Akzeptanzkriterien:**
- [ ] Ausgabe deterministisch bei identischer Seite (Test, zwei Läufe byte-gleich)
- [ ] `-i` reduziert eine Referenz-Seite um ≥ 70 % gegenüber dem vollen Baum
- [ ] Shadow DOM und `iframe`-Grenzen sind im Baum sichtbar markiert
**Nicht in diesem Issue:** Stabilität über Snapshots hinweg (→ B-16)

---
**B-10** · `titel:` Ref-Adressierung und Grundinteraktion
`labels:` area:refs, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-09
**Aufgabe:** `@eN` als Selektor überall dort akzeptieren, wo CSS-Selektoren
erlaubt sind. Kommandos: `click`, `dblclick`, `fill`, `type`, `press`, `hover`,
`focus`, `select`, `check`, `uncheck`, `scroll`, `scrollintoview`.
Eingabe über CDP-`Input`-Events (echte Trusted Events), nicht über JS-Dispatch.
**Akzeptanzkriterien:**
- [ ] `click @e2` funktioniert nach `snapshot` ohne erneuten Selektor
- [ ] Unbekannte Ref → strukturierter Fehler mit Hinweis auf `snapshot`
- [ ] `fill` leert das Feld zuvor, `type` hängt an — dokumentiert und getestet
- [ ] Klick scrollt das Ziel automatisch in den Viewport

---
**B-11** · `titel:` `get`-Familie und Zustandsprüfungen
`labels:` area:output, type:feature · `milestone:` v0.1.0 · `nach:` B-10
**Aufgabe:** `get text|html|value|attr|title|url|count|box|styles`,
`is visible|enabled|checked`. Alle mit `--json`.
**Akzeptanzkriterien:**
- [ ] Jedes Kommando funktioniert mit CSS-Selektor **und** `@eN`
- [ ] `is`-Kommandos setzen zusätzlich den Exit-Code (0 = wahr, 1 = falsch) für Shell-Nutzung
- [ ] `get styles` liefert berechnete Werte, nicht die Deklaration

---
**B-12** · `titel:` Screenshots
`labels:` area:output, type:feature · `milestone:` v0.1.0 · `nach:` B-10
**Aufgabe:** `screenshot [pfad]` für Viewport, `--full` für ganze Seite,
`--selector`/`@eN` für Elemente, `--format png|jpeg`, `--quality`,
`--screenshot-dir`. Ohne Pfad: Ablage im Cache-Verzeichnis, Pfad wird zurückgegeben.
**Akzeptanzkriterien:**
- [ ] Element-Screenshot beschneidet korrekt auf die Bounding-Box
- [ ] Rückgabe enthält Pfad, Maße und Bytegröße als JSON
- [ ] Pfad-Guard: kein Schreiben außerhalb erlaubter Verzeichnisse ohne explizites Flag

---
**B-13** · `titel:` Einheitliches Ausgabe- und Fehlerschema
`labels:` area:output, type:feature, prio:high · `milestone:` v0.1.0 · `nach:` B-06
**Aufgabe:** Globales `--json`. Erfolg: `{"success":true,"data":…,"warnings":[…]}`.
Fehler: `{"success":false,"error":{"code":"stale_ref","message":…,"hint":…,"details":{…}}}`.
Fehlercodes als Konstanten-Enum, dokumentiert. Mapping auf `corekit/exitcodes`.
**Akzeptanzkriterien:**
- [ ] Jeder Fehlerpfad liefert einen Code aus dem Enum, keine freien Strings
- [ ] Golden-File-Tests für Erfolg und Fehler
- [ ] Fehlercodes in `docs/errors.md` dokumentiert

---
**B-14** · `titel:` Release-Pipeline und v0.1.0
`labels:` area:ci, type:chore, prio:high · `milestone:` v0.1.0 · `nach:` B-03, B-13
**Aufgabe:** GoReleaser (6+ Ziele), Cosign-Signatur, Syft-SBOM, Checksums,
Homebrew-Tap-Eintrag in `danieljustus/homebrew-tap`, Release-Notes-Vorlage.
**Akzeptanzkriterien:**
- [ ] `brew install danieljustus/tap/symbrowse` installiert das Binary
- [ ] Artefakte sind signiert und mit SBOM versehen
- [ ] `symbrowse version --json` meldet die Release-Version

---
**B-15** · `titel:` Test-Fixtures: lokaler Referenz-Webserver
`labels:` area:ci, type:test, prio:high · `milestone:` v0.1.0 · `nach:` B-01
**Kontext:** Tests gegen das echte Web sind flaky und nicht reproduzierbar.
**Aufgabe:** `internal/testserver` mit Seiten für: statisches Dokument,
Formular (Text/Select/Checkbox/Radio/Datei), SPA mit verzögerter Hydration,
Overlay/Modal das Klicks abfängt, iframe-Verschachtelung, Shadow DOM,
versteckter Text (5 Varianten), `aria-label`-Mismatch, Endlos-Redirect,
langsame Antwort, 404/500.
**Akzeptanzkriterien:**
- [ ] Alle nachfolgenden Milestones testen ausschließlich gegen diesen Server
- [ ] Server startet in-process, kein fester Port
- [ ] Jede Fixture-Seite ist in `internal/testserver/README.md` beschrieben

---

### Milestone v0.2.0 — Agenten-Ergonomie

---
**B-16** · `titel:` Stabile Refs über Snapshots hinweg (Refkey + Tombstones)
`labels:` area:refs, type:feature, prio:high · `milestone:` v0.2.0 · `nach:` B-10
**Kontext:** ARCHITEKTUR.md §5.2 — der größte Unterschied zum Vorbild.
**Aufgabe:** `refkey = sha256(role ‖ accessible-name ‖ normalisierter DOM-Pfad ‖ Geschwister-Ordinal)`.
Persistente Zuordnung `refkey → @eN` pro Session. Gleicher Key → gleiche Nummer.
Verschwundener Key → Tombstone mit Grund (`removed`, `navigated`, `detached`).
Zugriff auf tote Ref liefert `stale_ref` mit vorherigem Rollen-/Namens-Kontext
und Hinweis auf `snapshot --diff`.
**Akzeptanzkriterien:**
- [ ] `@e7` bleibt `@e7` über Klick, Formulareingabe und teilweise Neu-Renderung
- [ ] Tombstone-Fehler nennt Rolle und Namen des verschwundenen Elements
- [ ] Nummern werden nie recycelt, solange die Session lebt
- [ ] Normalisierter DOM-Pfad ist robust gegen reine Reihenfolgeänderungen von Geschwistern

---
**B-17** · `titel:` `snapshot --diff`
`labels:` area:refs, area:output, type:feature, prio:high · `milestone:` v0.2.0 · `nach:` B-16
**Aufgabe:** Differenz zum letzten Snapshot derselben Session als `+` (neu),
`-` (entfernt), `~` (geändert: Name, Zustand, Wert, Sichtbarkeit).
`--diff --since <snapshot_id>` gegen einen bestimmten früheren Stand.
Bei fehlendem Vorgänger: vollständiger Snapshot mit Hinweisfeld.
**Akzeptanzkriterien:**
- [ ] Klick auf einen Tab-Wechsel liefert nur die tatsächlich geänderten Zeilen
- [ ] `--json` liefert `{added, removed, changed}` als Arrays
- [ ] Diff ist korrekt, auch wenn zwischenzeitlich navigiert wurde

---
**B-18** · `titel:` Benchmark: Ref-Erhalt und Diff-Kosten
`labels:` area:refs, type:test, prio:high · `milestone:` v0.2.0 · `nach:` B-17
**Aufgabe:** Messbarer Test gegen die Fixtures aus B-15: Anteil erhaltener Refs
nach einer Interaktion, Tokenkosten des Folgesnapshots mit und ohne `--diff`.
Ergebnis als Tabelle im Testoutput und in `docs/benchmarks.md`.
**Akzeptanzkriterien:**
- [ ] Ref-Erhalt ≥ 80 % über einen Klick auf allen Fixture-Seiten außer der SPA-Hydrations-Seite
- [ ] Folgesnapshot mit `--diff` im Median < 200 Token
- [ ] Test schlägt fehl, wenn die Schwellen unterschritten werden

---
**B-19** · `titel:` Token-Budget mit truncate-and-store
`labels:` area:output, type:feature, prio:high · `milestone:` v0.2.0 · `nach:` B-13
**Kontext:** ARCHITEKTUR.md §5.3, Muster aus `symfetch` (Hermes-Style).
**Aufgabe:** Token-Schätzer, `--max-tokens N` für alle ausgabestarken Kommandos.
Bei Überschreitung Kopf+Fuß zurückgeben, Vollinhalt in
`~/.cache/symbrowse/out/` ablegen, `{truncated, tokens_returned, tokens_total,
cache_id, hint}` mitliefern. `symbrowse cache get <id> [--range a-b]`,
`cache list`, `cache clear`. Strengere Defaults im MCP-Modus als im TTY-Modus.
**Akzeptanzkriterien:**
- [ ] Kein Kommando kann mehr als das Budget in den Kontext schreiben
- [ ] `cache get --range` liefert exakt den angeforderten Zeilenbereich
- [ ] Cache-Einträge verfallen nach konfigurierbarer Zeit (Default 24 h)

---
**B-20** · `titel:` `read`-Kommando im `symfetch`-Ausgabeschema
`labels:` area:output, type:feature, prio:high · `milestone:` v0.2.0 · `nach:` B-19
**Kontext:** ARCHITEKTUR.md §6.1 — ein Agent soll nur ein Schema lernen.
**Aufgabe:** `symbrowse read [url]` rendert die Seite und gibt Markdown mit
YAML-Frontmatter (`title`, `url`, `fetched_at`, `lang`, `schema_type`) oder JSON
aus. Feldnamen, Frontmatter-Schlüssel und Truncate-Semantik **identisch** zu
`symfetch`. Optionen `--filter`, `--outline`, `--selector`, `--raw`.
Eigener minimaler HTML→Markdown-Pfad in `internal/render` (Extraktion nach
corekit erst in B-61).
**Akzeptanzkriterien:**
- [ ] Konformitätstest: Feldnamen und Frontmatter-Schlüssel gegen eine im Repo
      abgelegte Schema-Datei geprüft (Kopie des Fetch-Schemas, Quelle dokumentiert)
- [ ] Für eine statische Seite unterscheidet sich das Ergebnis von `symfetch`
      nur in Whitespace (manuell verifiziert, Ergebnis im Issue dokumentiert)
- [ ] `--outline` liefert nur die Überschriftenstruktur

---
**B-21** · `titel:` Semantische Finder
`labels:` area:refs, type:feature · `milestone:` v0.2.0 · `nach:` B-10
**Aufgabe:** `find role <role> <aktion>`, `find text`, `find label`,
`find placeholder`, `find alt`, `find title`, `find testid`, `find first|last|nth`.
Aktionen: `click`, `fill`, `check`, `hover`, `text`, `ref` (nur Ref zurückgeben).
Optionen `--name`, `--exact`.
**Akzeptanzkriterien:**
- [ ] Mehrdeutigkeit liefert Fehler mit Auflistung aller Treffer inkl. Refs — nie stilles Raten
- [ ] `find … ref` erlaubt Auflösung ohne Aktion
- [ ] Rollen folgen der ARIA-Spezifikation, dokumentiert

---
**B-22** · `titel:` `batch` mit `--bail` und `--dry-run`
`labels:` area:output, type:feature · `milestone:` v0.2.0 · `nach:` B-13
**Aufgabe:** `symbrowse batch "cmd1" "cmd2" …` und JSON-Array über stdin.
Ergebnis als Array mit Einzelstatus. `--bail` bricht beim ersten Fehler ab.
`--dry-run` gibt den Ausführungsplan inkl. Risiko-Klassen zurück, **ohne** auszuführen.
**Akzeptanzkriterien:**
- [ ] Ein Batch-Aufruf ist messbar günstiger als n Einzelaufrufe
- [ ] `--dry-run` verändert den Seitenzustand nachweislich nicht
- [ ] Teilfehler brechen ohne `--bail` nicht den Gesamtlauf ab

---
**B-23** · `titel:` Content-Boundaries für nicht vertrauenswürdige Seiteninhalte
`labels:` area:security, type:feature, prio:high · `milestone:` v0.2.0 · `nach:` B-20
**Aufgabe:** Alle Ausgaben, die Seiteninhalt enthalten, in eindeutige Marker
fassen (`--content-boundaries`, im MCP-Modus Default an). Marker sind nicht
durch Seiteninhalt fälschbar (Nonce pro Antwort).
**Akzeptanzkriterien:**
- [ ] Eine Fixture-Seite, die den Marker im Text nachahmt, kann die Begrenzung nicht durchbrechen (Test)
- [ ] Marker enthalten Herkunfts-URL
- [ ] Im JSON-Modus als eigenes Feld statt als Inline-Text

---
**B-24** · `titel:` Prompt-Injection-Heuristik mit `warnings[]`
`labels:` area:security, type:feature, prio:high · `milestone:` v0.2.0 · `nach:` B-23
**Kontext:** ARCHITEKTUR.md §5.7.
**Aufgabe:** Erkennung von (a) verstecktem Text (`display:none`,
`visibility:hidden`, `font-size:0`, `opacity:0`, Off-Viewport, Farbe≈Hintergrund),
(b) agentengerichteten Imperativen (mehrsprachige Musterliste, konfigurierbar),
(c) `aria-label`-Mismatch auf interaktiven Elementen, (d) Instruktionen in
`alt`, `title`, HTML-Kommentaren, `<meta>`. Ergebnis als
`warnings[{kind, severity, ref, excerpt}]`. **Inhalt wird nie stillschweigend entfernt.**
**Akzeptanzkriterien:**
- [ ] Alle Fixture-Varianten aus B-15 werden erkannt
- [ ] Falsch-Positiv-Rate auf einer Sammlung normaler Seiten dokumentiert
- [ ] `--no-injection-scan` schaltet ab, Warnung wird dann protokolliert
- [ ] Musterliste liegt als Datei vor, nicht im Code

---
**B-25** · `titel:` Verdeckungs-Diagnose bei fehlgeschlagenen Klicks
`labels:` area:refs, type:feature · `milestone:` v0.2.0 · `nach:` B-10
**Aufgabe:** Wenn ein Klick nicht das Zielelement trifft, ermitteln, welches
Element an der Trefferkoordinate liegt, und im Fehler nennen (Rolle, Name, Ref).
Häufigster Fall: Cookie-Banner und Modals.
**Akzeptanzkriterien:**
- [ ] Fehler nennt das verdeckende Element und dessen Ref
- [ ] Hinweis schlägt die naheliegende Folgeaktion vor (z. B. Banner schließen)
- [ ] Test gegen die Overlay-Fixture

---

### Milestone v0.3.0 — MCP

---
**B-26** · `titel:` MCP-Server auf `corekit/mcpserver`
`labels:` area:mcp, type:feature, prio:high · `milestone:` v0.3.0 · `nach:` B-13
**Aufgabe:** `symbrowse mcp` als JSON-RPC-2.0-stdio-Server. Tool-Registrierung,
typisierte Eingabeschemata, strukturierte Antworten. Zero-Stdio-Pollution
strikt (Testfall: Log-Ausgabe während eines Tool-Calls bricht den Handshake nicht).
**Akzeptanzkriterien:**
- [ ] Handshake mit Claude Code, Cursor und OpenCode manuell verifiziert und dokumentiert
- [ ] Jedes Tool nimmt `session` entgegen
- [ ] Kein Byte auf stdout außer JSON-RPC-Frames (automatisierter Test)

---
**B-27** · `titel:` MCP-Tool-Profile
`labels:` area:mcp, type:feature, prio:high · `milestone:` v0.3.0 · `nach:` B-26
**Aufgabe:** `--tools core|nav|state|network|debug|flows|all`, Default `core`.
Profil-Zuordnung gemäß ARCHITEKTUR.md §6.2. Kommaseparierte Kombination erlaubt.
`symbrowse mcp --list-profiles` beschreibt jedes Profil und die Tool-Anzahl.
**Akzeptanzkriterien:**
- [ ] Default-Profil registriert < 15 Tools
- [ ] Profil-Zuordnung ist Datentabelle, nicht verstreute Bedingungen
- [ ] Test: jedes Tool gehört zu mindestens einem Profil, `all` enthält alle

---
**B-28** · `titel:` `versionkit`-Handshake und `version --json`
`labels:` area:mcp, type:chore · `milestone:` v0.3.0 · `nach:` B-26
**Aufgabe:** `{tool, version, schema_version}` gemäß Ökosystem-Konvention,
damit Hub/Brain/appkit-ToolKit andocken können.
**Akzeptanzkriterien:**
- [ ] `symbrowse version --json` entspricht dem `versionkit`-Schema
- [ ] `schema_version` wird bei jeder inkompatiblen Ausgabeänderung erhöht, Regel in AGENTS.md

---
**B-29** · `titel:` Domain-Allowlist im Netzwerk-Layer
`labels:` area:security, type:feature, prio:high · `milestone:` v0.3.0 · `nach:` B-05
**Aufgabe:** `--allowed-domains "example.com,*.example.com"` erzwingt auf
CDP-Ebene: Navigation, Subresources, WebSocket, EventSource, `sendBeacon`,
Worker-Bootstrap. WebRTC deaktiviert. Blockierte Anfragen werden gezählt und
im Ergebnis als `warnings[]` gemeldet.
**Akzeptanzkriterien:**
- [ ] Fixture mit Fremd-Domain-Subresource wird nachweislich blockiert
- [ ] Umgehungsversuch über `<meta refresh>`, `window.open` und Worker schlägt fehl (Tests)
- [ ] Bekannte Inkompatibilitäten (Chrome-Profil-Wiederverwendung, Auto-Connect) dokumentiert und beim Start gewarnt

---
**B-30** · `titel:` SSRF-Guard mit Fetch-kompatiblen Defaults
`labels:` area:security, type:feature, prio:high · `milestone:` v0.3.0 · `nach:` B-29
**Aufgabe:** RFC1918, Loopback, Link-Local, `.local`, IPv6-ULA blockieren.
Im MCP-Modus Default `deny`, per `--allow-private` explizit freizugeben.
DNS-Rebinding beachten: Auflösung zum Verbindungszeitpunkt prüfen.
**Akzeptanzkriterien:**
- [ ] `symbrowse mcp` kann `http://localhost:…` ohne Opt-in nicht öffnen
- [ ] DNS-Rebinding-Test (Hostname zeigt auf 127.0.0.1) schlägt fehl
- [ ] Verhalten und Defaults deckungsgleich mit `symfetch`, Abweichungen dokumentiert

---
**B-31** · `titel:` Eskalationsvertrag mit `symfetch`
`labels:` area:output, area:mcp, type:feature, cross-repo · `milestone:` v0.3.0 · `nach:` B-20
**Kontext:** ARCHITEKTUR.md §6.1. Gegenstück: X-1 in `symaira-fetch`.
**Aufgabe:** `symbrowse read --engine-hint` meldet, ob JS für den Inhalt
tatsächlich nötig war (`js_required: true|false`, mit Begründung), damit ein
Agent künftig direkt Tier 0 wählt. Dokumentierter Vertrag in `docs/tiers.md`
mit Entscheidungsbaum für Agenten.
**Akzeptanzkriterien:**
- [ ] Statische Fixture-Seite meldet `js_required: false`
- [ ] SPA-Fixture meldet `js_required: true` mit Begründung
- [ ] `docs/tiers.md` enthält den Entscheidungsbaum als Ablaufdiagramm

---
**B-32** · `titel:` MCP-Dokumentation und Beispielkonfigurationen
`labels:` area:docs, type:chore · `milestone:` v0.3.0 · `nach:` B-27
**Aufgabe:** `docs/mcp.md` mit Konfigurationsschnipseln für Claude Code, Cursor,
OpenCode, Claude Desktop; Profil-Empfehlungen je Anwendungsfall; Hinweise zu
Allowlist und SSRF-Defaults; ein durchgängiges Beispiel (Login-Wand → Snapshot →
Formular → Ergebnis).
**Akzeptanzkriterien:**
- [ ] Jeder Schnipsel wurde real getestet
- [ ] README verlinkt `docs/mcp.md` prominent

---

### Milestone v0.4.0 — Sessions, Zustand, Credentials

---
**B-33** · `titel:` Cookies und Web-Storage
`labels:` area:state, type:feature · `milestone:` v0.4.0 · `nach:` B-07
**Aufgabe:** `cookies`, `cookies set`, `cookies clear`, `cookies set --curl <datei>`;
`storage local|session [key]`, `… set <k> <v>`, `… clear`.
**Akzeptanzkriterien:**
- [ ] Cookie-Werte werden im JSON-Modus per Default maskiert, `--reveal` nötig
- [ ] Import aus curl-Format getestet
- [ ] Storage-Zugriff funktioniert pro Origin, nicht global

---
**B-34** · `titel:` Zustands-Persistenz (`state save|load|list|show|clear|clean`)
`labels:` area:state, type:feature, prio:high · `milestone:` v0.4.0 · `nach:` B-33
**Aufgabe:** Cookies + Storage als benannter Zustand unter
`~/.local/state/symbrowse/states/`. `state clean --older-than <tage>`,
Default-Ablauf 30 Tage (`SYMBROWSE_STATE_EXPIRE_DAYS`).
**Akzeptanzkriterien:**
- [ ] `state show` zeigt Metadaten (Origins, Anzahl Cookies, Alter) **ohne** Werte
- [ ] Dateirechte `0600`, atomares Schreiben über `corekit/fsutil`
- [ ] Abgelaufene Zustände werden beim Daemon-Start gemeldet

---
**B-35** · `titel:` Zustands-Verschlüsselung, Schlüssel aus `symvault`
`labels:` area:state, area:security, type:feature, prio:high · `milestone:` v0.4.0 · `nach:` B-34
**Kontext:** Das Vorbild verlangt eine 64-stellige Hex-Env-Variable — genau die
Art Secret-Handhabung, die `symvault` im Ökosystem abgelöst hat.
**Aufgabe:** AES-256-GCM für Zustandsdateien. Schlüsselbezug in dieser Reihenfolge:
(1) `symvault` per Laufzeit-Erkennung, (2) OS-Keychain, (3)
`SYMBROWSE_ENCRYPTION_KEY` als dokumentierter Notnagel. Kein Schlüssel im Klartext-Log.
**Akzeptanzkriterien:**
- [ ] Ohne Schlüssel ist eine verschlüsselte Zustandsdatei nicht ladbar (Test)
- [ ] Schlüsselherkunft in `state show` sichtbar
- [ ] Läuft vollständig ohne installiertes `symvault` (Fallback-Test)

---
**B-36** · `titel:` `--restore` mit Auto-Save-Politik
`labels:` area:state, type:feature · `milestone:` v0.4.0 · `nach:` B-35
**Aufgabe:** `--restore [key]` lädt und speichert Zustand automatisch.
Politik `auto|always|never`, Autosave-Mindestintervall (Default 30 s, `0` = nur
beim Schließen).
**Akzeptanzkriterien:**
- [ ] Session überlebt Daemon-Neustart mit erhaltenem Login (Test gegen Fixture-Login)
- [ ] Autosave verursacht keine messbare Latenz in Interaktionskommandos
- [ ] `never` schreibt nachweislich nichts

---
**B-37** · `titel:` Worktree-bezogene Session-IDs
`labels:` area:state, type:feature · `milestone:` v0.4.0 · `nach:` B-07
**Kontext:** Passt zum bestehenden `parallel-repo-agents`-Muster — mehrere
Agenten arbeiten gleichzeitig in verschiedenen Worktrees desselben Repos.
**Aufgabe:** `symbrowse session id --scope worktree|repo|cwd --prefix <name>`
erzeugt eine stabile, kollisionsfreie Session-ID.
**Akzeptanzkriterien:**
- [ ] Zwei Worktrees desselben Repos erhalten verschiedene IDs, derselbe Worktree immer dieselbe
- [ ] Funktioniert außerhalb eines Git-Repos mit dokumentiertem Fallback
- [ ] `session list` zeigt Scope und Ursprungspfad

---
**B-38** · `titel:` Chrome-Profil-Wiederverwendung
`labels:` area:state, type:feature · `milestone:` v0.4.0 · `nach:` B-05
**Aufgabe:** `--profile <name|pfad>` nutzt ein bestehendes Chrome-Profil
(bereits eingeloggte Sitzungen des Menschen). `symbrowse profiles` listet
gefundene Profile.
**Akzeptanzkriterien:**
- [ ] Deutliche Warnung beim Start: laufender Chrome sperrt das Profil; Allowlist ist in diesem Modus nicht durchsetzbar
- [ ] Profil wird nie ohne explizites Flag benutzt
- [ ] Read-only-Kopie als Option, damit das Original unverändert bleibt

---
**B-39** · `titel:` Credential-Eingabe über `symvault` ohne Klartext
`labels:` area:security, area:state, type:feature, prio:high · `milestone:` v0.4.0 · `nach:` B-35
**Kontext:** ARCHITEKTUR.md §5.5/E4. Es entsteht **kein** eigener Tresor.
**Aufgabe:** `symbrowse auth login <vault-eintrag> [--url …]` löst den Eintrag
über `symvault` auf und tippt Benutzername/Passwort per CDP-`Input` in die
erkannten Felder. Der Wert erscheint nie in Rückgabe, Log, Journal oder Trace.
Ohne `symvault`: klarer Fehler mit Anleitung, **kein** Klartext-Flag als Ersatz.
**Akzeptanzkriterien:**
- [ ] Redaction-Test: Secret taucht in keinem Ausgabekanal, Logfile oder Trace auf
- [ ] Feld-Erkennung funktioniert bei Standard-Login-Formularen der Fixture; bei Fehlschlag Fehler mit Snapshot-Auszug
- [ ] Kommando trägt Risiko-Klasse `credential`
- [ ] Ohne installiertes `symvault` baut und läuft alles Übrige unverändert

---
**B-40** · `titel:` `set`-Familie: Viewport, Gerät, Geolocation, Header, Medien
`labels:` area:engine, type:feature · `milestone:` v0.4.0 · `nach:` B-05
**Aufgabe:** `set viewport <w> <h> [scale]`, `set device <name>`,
`set geo <lat> <lng>`, `set offline [on|off]`, `set headers <json>`,
`set media dark|light`, `set user-agent`.
**Akzeptanzkriterien:**
- [ ] Geräteliste als Datendatei, nicht im Code
- [ ] `set headers` kann keine Authorization-Header aus dem Agenten-Kontext setzen, ohne Risiko-Klasse `credential` auszulösen
- [ ] Einstellungen überleben Navigation innerhalb der Session

---

### Milestone v0.5.0 — Mensch ↔ Agent

---
**B-41** · `titel:` Aktions-Journal (append-only)
`labels:` area:oob, type:feature, prio:high · `milestone:` v0.5.0 · `nach:` B-13
**Aufgabe:** Jede Aktion protokollieren: Zeitstempel, Kommando, Argumente
(redigiert), Ziel-Refkey, URL vorher/nachher, Risiko-Klasse, Entscheider
(`policy`/`human`/`guard`), Ergebnis, Dauer. JSONL unter
`~/.local/state/symbrowse/journal/<session>.jsonl`, append-only, `0600`.
Format bewusst kompatibel zu dem, was `symaira-room` als signiertes Journal plant.
**Akzeptanzkriterien:**
- [ ] Kein Secret und kein Klartext-Passwort im Journal (Test)
- [ ] `symbrowse journal tail [--session]` und `journal show --json`
- [ ] Schema versioniert, in `docs/journal.md` dokumentiert

---
**B-42** · `titel:` `trace export` und deterministisches `trace replay`
`labels:` area:oob, type:feature, prio:high · `milestone:` v0.5.0 · `nach:` B-41
**Aufgabe:** Journal → wiederholbare Trace-Datei (Aktionen inkl. Selektor-Auflösung
und erwarteter Zustände). `trace replay <datei>` fährt sie nach und meldet je
Schritt Übereinstimmung oder Abweichung.
**Akzeptanzkriterien:**
- [ ] Ein aufgezeichneter Formular-Durchlauf ist reproduzierbar nachfahrbar
- [ ] Abweichung nennt Schritt, erwarteten und tatsächlichen Zustand
- [ ] Secrets werden beim Replay erneut über `symvault` aufgelöst, nie aus der Datei gelesen

---
**B-43** · `titel:` Risiko-Klassifikation und lokale Policy
`labels:` area:security, type:feature, prio:high · `milestone:` v0.5.0 · `nach:` B-41
**Aufgabe:** Jedes Kommando trägt eine Risiko-Klasse (`read`, `navigate`,
`interact`, `submit`, `eval`, `credential`, `download`, `upload`, `network-mock`)
gemäß ARCHITEKTUR.md §5.5. `policy.toml` bildet Klasse × Domain →
`allow|confirm|deny` ab. Defaults unterscheiden MCP- und TTY-Modus.
Formular-Absenden wird als `submit` erkannt (Klick auf `type=submit`,
`Enter` in einem Formularfeld, `form.requestSubmit`).
**Akzeptanzkriterien:**
- [ ] Klassenzuordnung ist eine Tabelle; ein Test erzwingt, dass **jedes** registrierte Kommando eine Klasse hat
- [ ] `symbrowse policy explain <kommando> --url <url>` zeigt die greifende Regel und ihre Herkunft
- [ ] Default-Policy in `docs/security.md` dokumentiert

---
**B-44** · `titel:` OOB-Kanal: Overlay, Notification, Blockier-Semantik
`labels:` area:oob, type:feature, prio:high · `milestone:` v0.5.0 · `nach:` B-43
**Kontext:** ARCHITEKTUR.md §5.4 — ein Mechanismus für Handoff, Approval und Watch.
**Aufgabe:** In die Seite injizierbares Overlay (Shadow-DOM-isoliert, von
Seiten-CSS nicht manipulierbar) mit Grundtext, Grund, Timer und Buttons
*Fertig* / *Abbrechen*. macOS-Notification. Blockierende Wartesemantik mit
Timeout und sauberem Abbruch. Zustände: `pending`, `completed`, `cancelled`,
`timeout`. `symbrowse oob status --json` zum Abfragen von außen.
**Akzeptanzkriterien:**
- [ ] Overlay ist durch Seiten-CSS/JS nicht verdeckbar oder entfernbar (Test gegen feindliche Fixture)
- [ ] Timeout liefert strukturiertes Ergebnis, keinen Hänger
- [ ] Funktioniert auch headless: dann Notification + `oob status` statt Overlay
- [ ] Jede Übergabe führt die Ownership-State-Machine `agent → agent_delegated → user` über explizite `handoff`/`claim`-Aktionen fort; der Übergang wird journalisiert
- [ ] `session_user_control`, `session_inactive` und `handoff_timeout` liefern `retryable`, `requires_user_confirmation` und `resume_hint` im gemeinsamen Fehler-Envelope

---
**B-45** · `titel:` `handoff` — Übergabe an den Menschen ohne Sitzungsverlust
`labels:` area:oob, type:feature, prio:high · `milestone:` v0.5.0 · `nach:` B-44, B-36
**Aufgabe:** `symbrowse handoff --reason <text> [--timeout 5m]`. Headless-Session
wird sichtbar: Zustand sichern, Chrome headed neu starten, Zustand restaurieren,
zur selben URL navigieren, Overlay zeigen. Nach Abschluss Rückgabe
`{status, duration_ms, url_after, snapshot_diff}`.
**Akzeptanzkriterien:**
- [ ] Cookies, Storage und Refs überleben den Wechsel headless → headed → weiter (Test)
- [ ] Wurde bereits headed gestartet, entfällt der Neustart
- [ ] Ein 2FA-Login gegen die Fixture ist end-to-end durchführbar
- [ ] Journal-Eintrag mit Entscheider `human` und der angegebenen Begründung
- [ ] `claim`, bestätigtes `takeover` und `complete {keep}` sind explizite Operationen; eine fehlende Takeover-Bestätigung lässt den User-Zustand unverändert
- [ ] Daemon-Neustart und Client-Reconnect stellen dieselbe `session_id` und `control_id` wieder her

---
**B-46** · `titel:` Freigaben über OOB statt TTY-Prompt
`labels:` area:oob, area:security, type:feature, prio:high · `milestone:` v0.5.0 · `nach:` B-45
**Kontext:** Im MCP-Modus ist stdin der JSON-RPC-Kanal — ein TTY-Prompt ist
unmöglich. Das Vorbild verweigert dort schlicht. Wir fragen über den OOB-Kanal.
**Aufgabe:** Policy-Ergebnis `confirm` löst eine OOB-Freigabefrage aus
(Kommando, Ziel, Domain, Risiko-Klasse, Injection-Warnungen der Seite).
Bei Timeout gilt `deny`. Optional `--confirm-scope once|session|domain`.
**Akzeptanzkriterien:**
- [ ] Freigabe funktioniert nachweislich im MCP-Modus ohne TTY
- [ ] Timeout führt zu `deny` mit strukturiertem Fehler, nie zu stillem `allow`
- [ ] Erteilte Freigaben landen mit Umfang und Gültigkeit im Journal
- [ ] Hard-Stop-Antworten werden bei wiederholten Aufrufen identisch geliefert und erzeugen keine zusätzliche normale Ausgabe

---
**B-47** · `titel:` `watch` — Mitschau für den Menschen
`labels:` area:oob, type:feature · `milestone:` v0.5.0 · `nach:` B-44
**Aufgabe:** `symbrowse watch [--session]` öffnet die laufende Agentensitzung
sichtbar und read-only (Eingaben des Menschen werden nicht an den Agenten
weitergereicht) und streamt das Aktions-Journal ins Terminal.
`watch --take-over` wechselt in einen Handoff.
**Akzeptanzkriterien:**
- [ ] Mitschau beeinflusst die Agentensitzung nicht (Test: Refs bleiben stabil)
- [ ] Journal-Stream zeigt Risiko-Klasse und Entscheider je Zeile
- [ ] `--take-over` erzeugt einen regulären Handoff-Journaleintrag

---
**B-48** · `titel:` `symguard`-Delegation
`labels:` area:security, type:feature, cross-repo, prio:high · `milestone:` v0.5.0 · `nach:` B-43
**Kontext:** ARCHITEKTUR.md §5.5/E5 — Browse klassifiziert, Guard entscheidet.
Gegenstück: X-5 in `symaira-guard`.
**Aufgabe:** Laufzeit-Erkennung von `symguard`. Ist es vorhanden und
konfiguriert, wird die Entscheidung dorthin delegiert (Kommando, Klasse, Domain,
Warnungen als Eingabe) und das Urteil respektiert. Ohne Guard greift die lokale
Policy aus B-43. Entscheider wird im Journal vermerkt.
**Akzeptanzkriterien:**
- [ ] Funktioniert vollständig ohne `symguard` (Fallback-Test)
- [ ] Kein Compile-Time-Import von `symaira-guard`
- [ ] Guard-Ausfall führt zu `deny` mit klarer Meldung, nicht zu stillem `allow`
- [ ] `symbrowse policy explain` zeigt an, wer entschieden hat

---

### Milestone v0.6.0 — Flows

---
**B-49** · `titel:` Flow-Schema und Parser
`labels:` area:flows, type:feature, prio:high · `milestone:` v0.6.0 · `nach:` B-22
**Aufgabe:** YAML-Schema gemäß ARCHITEKTUR.md §5.6: `name`, `version`,
`domains`, `inputs`, `steps` (`open`, `find`, `click`, `fill`, `wait`, `assert`,
`snapshot`), `outputs`. Secret-Referenzen ausschließlich als `op://…`.
Validierung mit präzisen Fehlern (Zeile, Feld, Grund).
**Akzeptanzkriterien:**
- [ ] `symbrowse flow validate <datei>` mit Zeilen-genauen Fehlern
- [ ] Ein Flow mit Klartext-Secret wird abgelehnt
- [ ] `domains` schränkt die Ausführung hart ein
- [ ] Schema in `docs/flows.md` dokumentiert, mit JSON-Schema-Datei im Repo

---
**B-50** · `titel:` Flow-Runner
`labels:` area:flows, type:feature, prio:high · `milestone:` v0.6.0 · `nach:` B-49
**Aufgabe:** `symbrowse flow run <name> --input k=v …`. Schrittweise Ausführung,
Assertions als harte Abbruchbedingungen, Outputs aus Endzustand extrahieren,
Journal-Einträge je Schritt, Risiko-Klasse pro Schritt aus dem zugrunde
liegenden Kommando.
**Akzeptanzkriterien:**
- [ ] Ein Flow gegen die Formular-Fixture läuft ohne Modellzugriff durch
- [ ] Fehlende Pflicht-Inputs führen vor der Ausführung zum Abbruch
- [ ] `--dry-run` gibt den Plan mit Risiko-Klassen aus
- [ ] Flow-Lauf ist als ein Journal-Vorgang gruppiert

---
**B-51** · `titel:` `flow record` — Sitzung zu Flow-Entwurf
`labels:` area:flows, type:feature, prio:high · `milestone:` v0.6.0 · `nach:` B-50
**Kontext:** Der eigentliche Hebel: Der Agent exploriert einmal, der Mensch
segnet ab, danach läuft es billig und reproduzierbar.
**Aufgabe:** `flow record start|stop` zeichnet die Aktionen einer Session auf und
erzeugt einen Flow-Entwurf: konkrete Werte werden zu `inputs` verallgemeinert,
Secrets zu `op://`-Referenzen, beobachtete Endzustände zu `assert`-Schritten,
Refs zu semantischen Findern (weil Refs sitzungsgebunden sind).
**Akzeptanzkriterien:**
- [ ] Aufgezeichneter Formular-Durchlauf läuft als generierter Flow unverändert erneut
- [ ] Keine `@eN`-Refs im erzeugten Flow — ausschließlich semantische Selektoren
- [ ] Aufgezeichnete Secrets erscheinen nur als Referenz
- [ ] Entwurf enthält Kommentare, die dem Menschen die Prüfung erleichtern

---
**B-52** · `titel:` `symskills`-Anbindung für Flow-Discovery
`labels:` area:flows, type:feature, cross-repo · `milestone:` v0.6.0 · `nach:` B-51
**Kontext:** SSOT-Regel — kein zweites Registry. Gegenstück: X-4 in `symaira-skills`.
**Aufgabe:** Flows werden über `symskills` gefunden und verteilt, wenn es
installiert ist; sonst aus `~/.config/symbrowse/flows/` und
`./.symbrowse/flows/`. `flow list` zeigt Herkunft je Flow.
**Akzeptanzkriterien:**
- [ ] Funktioniert vollständig ohne `symskills`
- [ ] Projektlokale Flows haben Vorrang vor globalen, Herkunft ist sichtbar
- [ ] Kein Compile-Time-Import von `symaira-skills`

---
**B-53** · `titel:` Flow-Fehlerdiagnose
`labels:` area:flows, type:feature · `milestone:` v0.6.0 · `nach:` B-50
**Aufgabe:** Schlägt ein Schritt fehl, liefert der Runner: Schrittindex,
Selektor, erwarteter vs. tatsächlicher Zustand, Snapshot-Diff zum letzten
erfolgreichen Schritt, Screenshot (optional), und einen Reparaturvorschlag
(z. B. „Selektor traf 3 Elemente" oder „Overlay @e12 verdeckte das Ziel").
**Akzeptanzkriterien:**
- [ ] Fehlerausgabe ist ohne Zugriff auf die Seite verständlich
- [ ] Diagnose reicht aus, damit ein Agent den Flow selbst korrigieren kann (an drei absichtlich kaputten Flows verifiziert)

---

### Milestone v1.0.0 — Reichweite & Härtung

---
**B-54** · `titel:` Tabs, Fenster, Frames, Dialoge
`labels:` area:engine, type:feature · `milestone:` v1.0.0 · `nach:` B-07
**Aufgabe:** `tab` (Liste), `tab new [--label] [url]`, `tab <t1|label>`,
`tab close`, `window new`; `frame <sel>` / `frame main`;
`dialog accept [text]` / `dismiss` / `status`, Auto-Dismiss abschaltbar.
**Akzeptanzkriterien:**
- [ ] Refs sind pro Tab getrennt und bleiben beim Tab-Wechsel gültig
- [ ] Verschachtelte iframes ansprechbar (Test gegen Fixture)
- [ ] `beforeunload`-Dialoge blockieren die Automatisierung nicht

---
**B-55** · `titel:` Netzwerk: Routing, Mocking, Inspektion, HAR
`labels:` area:engine, area:security, type:feature · `milestone:` v1.0.0 · `nach:` B-29
**Aufgabe:** `network route <url> [--abort|--body <json>|--status <n>]`,
`network unroute`, `network requests [--filter|--type|--method|--status]`,
`network request <id>`, `network har start|stop [--content all|none]`.
Risiko-Klasse `network-mock`, im MCP-Modus per Default `deny`.
**Akzeptanzkriterien:**
- [ ] HAR-Datei ist in Chrome DevTools ladbar
- [ ] Request-Liste maskiert per Default `Authorization`- und `Cookie`-Header
- [ ] Mocking ist ohne explizite Policy-Freigabe nicht möglich

---
**B-56** · `titel:` Console, Fehler und `eval`
`labels:` area:engine, area:security, type:feature · `milestone:` v1.0.0 · `nach:` B-43
**Aufgabe:** `console [--json] [--clear]`, `errors [--clear]`,
`eval <js>` (`-b` base64, `--stdin`). `eval` trägt Risiko-Klasse `eval`, im
MCP-Modus per Default `confirm`.
**Akzeptanzkriterien:**
- [ ] `eval` ohne Freigabe im MCP-Modus schlägt fehl
- [ ] Console-Puffer ist beschränkt und respektiert das Token-Budget
- [ ] Uncaught Exceptions enthalten Stacktrace

---
**B-57** · `titel:` Accessibility-Audit über axe-core
`labels:` area:output, type:feature · `milestone:` v1.0.0 · `nach:` B-09
**Aufgabe:** axe-core eingebettet (`go:embed`), `symbrowse a11y [url]`
mit `--tags wcag2a,wcag2aa`, `--selector`, `--json`. Ausgabe wie im Vorbild:
`violations[]` mit `id`, `impact`, `nodes[]`.
**Akzeptanzkriterien:**
- [ ] Funktioniert offline, keine CDN-Abhängigkeit
- [ ] axe-core-Version im Output ausgewiesen und in `docs/` dokumentiert
- [ ] Ergebnis respektiert das Token-Budget

---
**B-58** · `titel:` `diff` für Snapshots, Screenshots und URLs
`labels:` area:output, type:feature · `milestone:` v1.0.0 · `nach:` B-17
**Aufgabe:** `diff snapshot [--baseline <datei>]`,
`diff screenshot --baseline <png>` (Pixelvergleich mit Toleranz),
`diff url <url1> <url2>`.
**Akzeptanzkriterien:**
- [ ] Screenshot-Diff liefert Abweichungsanteil und ein Differenzbild
- [ ] Für CI nutzbar: Exit-Code signalisiert Abweichung oberhalb der Schwelle

---
**B-59** · `titel:` Upload und Download mit Pfad-Guard
`labels:` area:security, type:feature · `milestone:` v1.0.0 · `nach:` B-43
**Aufgabe:** `upload <sel> <dateien…>`, Download-Verzeichnis konfigurierbar,
Download-Ereignisse im Journal. Beide Klassen im MCP-Modus per Default `confirm`.
Pfad-Guard über `corekit/fsutil` (kein Traversal, kein Schreiben außerhalb des
konfigurierten Verzeichnisses).
**Akzeptanzkriterien:**
- [ ] Traversal-Versuche (`../`, Symlinks, absolute Pfade) schlagen fehl (Tests)
- [ ] Downloads werden mit Herkunfts-URL, Größe und Prüfsumme protokolliert
- [ ] Upload außerhalb erlaubter Verzeichnisse wird verweigert

---
**B-60** · `titel:` Zweite Engine als Beweis der Abstraktion
`labels:` area:engine, type:feature · `milestone:` v1.0.0 · `nach:` B-05
**Aufgabe:** Eine zweite Engine (Kandidat: Lightpanda als schneller, JS-fähiger
Headless-Kern für reine Lese-Vorgänge) hinter demselben `Engine`-Interface,
wählbar per `--engine`. Ein Fähigkeiten-Feld meldet, was die Engine **nicht**
kann; nicht unterstützte Kommandos liefern einen klaren Fehler statt falscher
Ergebnisse.
**Akzeptanzkriterien:**
- [ ] Kein Paket außerhalb `internal/engine` musste für die zweite Engine geändert werden
- [ ] Fähigkeitsmatrix in `docs/engines.md`
- [ ] `read` liefert auf beiden Engines strukturell gleiches Markdown

---
**B-61** · `titel:` `domkit`-Extraktion nach `symaira-corekit`
`labels:` area:output, type:chore, cross-repo · `milestone:` v1.0.0 · `nach:` B-20, B-60
**Kontext:** ARCHITEKTUR.md §6.1/E9 — jetzt gibt es zwei reale Konsumenten
(`symfetch`, `symbrowse`), also darf extrahiert werden. Gegenstück: X-3.
**Aufgabe:** Semantische DOM-Pipeline (DomFilter, Content-Scoring,
Kategorisierung, TokenCompressor, Markdown-Renderer) aus `symaira-fetch` nach
`corekit/domkit` heben; `symbrowse` migriert von `internal/render` auf `domkit`.
**Akzeptanzkriterien:**
- [ ] Schnittstelle ist aus **beiden** realen Nutzungen abgeleitet, nicht geraten
- [ ] `symfetch`-Tests bleiben grün (im Fetch-Repo verifiziert)
- [ ] `symbrowse read` und `symfetch` erzeugen für dieselbe statische Seite identisches Markdown
- [ ] `corekit` bleibt frei von browser- und fingerprint-spezifischem Code

---
**B-62** · `titel:` Selbst-Update
`labels:` area:ci, type:feature · `milestone:` v1.0.0 · `nach:` B-14
**Aufgabe:** `corekit/updatecheck` (opt-in, max. 1×/24 h) plus
`updatecheck/updateapply` für `symbrowse upgrade` inklusive Cosign-Verifikation,
Install-Methoden-Erkennung und atomarem Austausch mit Rollback.
**Akzeptanzkriterien:**
- [ ] Update-Hinweis erscheint nie auf stdout im MCP-Modus
- [ ] Homebrew-Installation verweist auf `brew upgrade` statt selbst zu ersetzen
- [ ] Fehlgeschlagenes Update rollt zurück

---
**B-63** · `titel:` Testhärtung: Coverage-Schwelle, Fuzzing, E2E-Smoke
`labels:` type:test, area:ci, prio:high · `milestone:` v1.0.0 · `nach:` B-15
**Aufgabe:** Coverage-Gate in CI (Vorschlag: gesamt ≥ 65 %, `refs` ≥ 85 %,
`policy` ≥ 85 %, `injection` ≥ 80 %). Fuzz-Targets für Refkey-Normalisierung,
Snapshot-Serialisierung und Flow-Parser. E2E-Smoke-Test über das gebaute Binary
gegen den Fixture-Server (echter Chrome, in CI).
**Akzeptanzkriterien:**
- [ ] Schwellen in CI erzwungen
- [ ] Fuzz-Targets laufen 60 s je Ziel im wöchentlichen Lauf
- [ ] Smoke-Test deckt die Kette open → snapshot → fill → submit → read ab

---
**B-64** · `titel:` Dokumentation, Social Preview, Ökosystem-Eintrag
`labels:` area:docs, type:chore, cross-repo, prio:high · `milestone:` v1.0.0 · `nach:` B-60
**Aufgabe:** README mit Positionierung und Tier-Modell, `docs/`
(`tiers.md`, `mcp.md`, `security.md`, `flows.md`, `engines.md`, `journal.md`,
`errors.md`, `benchmarks.md`), Social-Preview-Grafik im Stil der übrigen Repos,
Eintrag in `../AGENTS.md` (Package Inventory + Developer Commands) und
`../ECOSYSTEM.md`.
**Akzeptanzkriterien:**
- [ ] README erklärt in den ersten fünf Zeilen die Abgrenzung zu `symfetch`
- [ ] Jedes Dokument aus der Liste existiert und ist verlinkt
- [ ] Root-`AGENTS.md` und `ECOSYSTEM.md` sind aktualisiert (Gegenstück X-6)

---

## 4. Cross-Repo-Issues

Diese Issues werden **nicht** in `symaira-browse` angelegt, sondern im
genannten Ziel-Repo. Jeweils mit Label `cross-repo` und einem Rückverweis auf
das korrespondierende Browse-Issue.

| ID | Repo | Titel | Inhalt | Passend zu |
|---|---|---|---|---|
| **X-1** | `symaira-fetch` | Eskalations-Hinweis auf `symbrowse` bei SPA-Skeleton und dünnem Inhalt | Bei erkanntem SPA-Skeleton, Thin-Content oder JS-Challenge zusätzlich `{"escalate":{"tool":"symbrowse","reason":…,"command":…}}` ausgeben. Kein harter Fehler, kein Aufruf — nur ein Hinweis, den ein Agent lesen kann. `AGENTS.md` dort vermerkt bereits „JS execution is a future milestone" — dieses Issue löst den Verweis ein. | B-31 |
| **X-2** | `symaira-fetch` | Ausgabeschema als versionierten Vertrag dokumentieren | Frontmatter-Schlüssel und JSON-Feldnamen in `docs/output-schema.md` festschreiben, `schema_version` einführen. Browse und künftige Konsumenten prüfen dagegen. | B-20 |
| **X-3** | `symaira-corekit` | `domkit`: semantische DOM-Pipeline aus Fetch extrahieren | Erst starten, wenn B-61 ansteht (zwei reale Konsumenten). Muss frei von Fingerprinting- und Browser-Spezifika bleiben (Corekit-Kontrakt). | B-61 |
| **X-4** | `symaira-skills` | Skill-Typ „flow" für ausführbare Browser-Abläufe | Ein Skill mit ausführbarem YAML-Rumpf, den ein Werkzeug (hier `symbrowse`) ausführt. Discovery, Versionierung und Verteilung bleiben in `symskills`. | B-52 |
| **X-5** | `symaira-guard` | Entscheidungs-Schnittstelle für externe Klassifizierer | Ein Aufrufer liefert Kommando, Risiko-Klasse, Domain und Warnungen; Guard antwortet `allow|confirm|deny` mit Begründung und schreibt seinen Audit-Eintrag. Erster Konsument ist `symbrowse` (B-48). | B-48 |
| **X-6** | Root (`Symaira Dev`) | `symaira-browse` in Root-`AGENTS.md` und `ECOSYSTEM.md` aufnehmen | Package Inventory (Go 1.26.5, CLI + MCP, `symbrowse`, Apache-2.0), Developer Commands, Abschnitt „Existing Instruction Files", Tier-Modell Fetch/Browse in `ECOSYSTEM.md`. | B-64 |
| **X-7** | `symaira-hub` | Hub-Modul „Browse": Live-Viewport, Journal, Approve/Deny | Erst nach v1.0. Konsumiert ausschließlich die JSON-Schnittstellen aus B-41/B-44/B-47 über `symaira-appkit`-ToolKit. Keine eigene Fachlogik in der GUI. | nach v1.0 |

---

## 5. Abhängigkeitsgraph (Kritischer Pfad)

```
B-01 → B-02 → B-04 → B-05 → B-06 → B-07 → B-08 → B-09 → B-10
                                                          │
                        ┌─────────────────────────────────┤
                        ▼                                 ▼
                     B-16 → B-17 → B-18              B-13 → B-19 → B-20
                        │                                    │
                        └──────────────┬─────────────────────┘
                                       ▼
                              B-26 → B-27 (MCP)
                                       │
                              B-33 → B-34 → B-35 → B-36 (Zustand)
                                       │        └──→ B-39 (Credentials)
                                       ▼
                              B-41 → B-43 → B-44 → B-45 → B-46
                                                    │
                                                    ▼
                                             B-49 → B-50 → B-51
                                                    │
                                                    ▼
                                             B-60 → B-61 → B-64
```

**Reihenfolge-Hinweise für die Umsetzung:**

- **B-15 (Fixtures) sofort nach B-01 bauen.** Jedes weitere Issue testet dagegen;
  nachträglich eingezogen kostet es das Doppelte.
- **B-16/B-17 sind der Kern der Produktidee.** Wenn eine Milestone geschoben
  werden muss, dann nicht diese.
- **B-43 (Risiko-Klassen) vor jedem Kommando aus M6.** Sonst müssen alle
  Kommandos nachträglich klassifiziert werden.
- **B-35 vor B-39.** Credential-Handhabung ohne Zustands-Verschlüsselung wäre
  eine Sicherheitslücke auf Zeit.
- **B-61 nicht vorziehen.** Die Extraktion vor B-60 hätte nur einen echten
  Konsumenten und würde die Schnittstelle falsch schneiden.

---

## 6. Was bewusst nicht geplant ist

Damit ein ausführender Agent nicht „hilfreich" Lücken füllt — diese Punkte sind
**begründete Nicht-Ziele** (ARCHITEKTUR.md §3.2 und §8) und dürfen nicht als
Issues angelegt werden:

`chat`-REPL / eingebaute LLM-Aufrufe · Web-Dashboard · eigener Credential-Tresor ·
stdio-Plugin-Protokoll · React-Introspektion · Web Vitals · CPU-Profiler ·
Chrome-Tracing · Cloud-Browser-Provider · CAPTCHA-Automatisierung ·
TLS-Fingerprint-Umgehung · Firefox/WebKit · verteiltes Crawling · eigenes GUI.

Fällt eine dieser Grenzen später, gehört das in eine überarbeitete
ARCHITEKTUR.md — nicht in ein Issue.

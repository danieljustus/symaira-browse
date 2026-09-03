# Prompt-Injection-Scan

`snapshot` prüft den Seiteninhalt standardmäßig heuristisch auf
Prompt-Injection-Vektoren (Issue #28). Das Ergebnis
sind `warnings[{kind, severity, ref, excerpt}]` auf der Ausgabe — **Inhalt
wird nie stillschweigend entfernt oder verändert**, nur gemeldet.

```sh
symbrowse snapshot                 # Scan läuft (Default)
symbrowse snapshot --no-injection-scan   # abschalten (Warnung wird geloggt)
symbrowse snapshot --injection-patterns ./meine-muster.txt   # eigene Musterliste
```

Die Musterliste liegt als Datei (`internal/injection/patterns.txt`,
mehrsprachig: EN/DE/FR/ES), nicht im Code. Eigene Listen ersetzen die
eingebettete Liste vollständig; Format: ein Ausdruck pro Zeile, `#`-Kommentare,
Groß-/Kleinschreibung egal, Leerraum wird normalisiert.

## Erkennungsklassen

| Kind | Severity | Erkannt wird |
|---|---|---|
| `hidden_text` | medium | Versteckter Text: `display:none`, `visibility:hidden`, `font-size:0`, `opacity:0`, Off-Viewport (`position:absolute` mit stark negativem Offset), Vordergrund≈Hintergrund (Hex/Named Colors, Toleranz 10) — inline oder per `<style>`-Regel |
| `imperative` | high | Agentengerichtete Imperative im sichtbaren Text (Musterliste, mehrsprachig) |
| `aria_mismatch` | high | Sichtbarer Text ≠ `aria-label` auf interaktiven Elementen (Klick-Umleitungsangriff); Icon-Buttons ohne sichtbaren Text sind ausgenommen |
| `attribute` | medium | Imperative in `alt`- oder `title`-Attributen |
| `comment` | low | Imperative in HTML-Kommentaren |
| `meta` | medium | Imperative in `<meta name/property=… content=…>` |

`ref` ist ein Locator (`#id`, `tag.class`, `tag:nth-of-type(n)`), `excerpt`
der erste Treffer (max. 120 Zeichen).

## Falsch-Positiv-Rate (gemessen)

Der automatische Test `TestScanNormalPagesFalsePositiveRate` scannt eine
Sammlung normaler Fixture-Seiten (static, form, overlay, iframe, shadow-dom,
spa) aus `internal/testserver`:

| Sammlung | Seiten | Warnings |
|---|---|---|
| Normale Fixture-Seiten | 6 | **0** |

Die Rate ist niedrig, weil nur mehrwortige, agentengerichtete Phrasen
matchen (keine Einzelwörter) und `hidden_text` auf Elemente mit
Textinhalt begrenzt ist. Zu erwartende Falsch-Positive: Seiten, die
legitim versteckte Textblöcke enthalten (z. B. SEO-Inhalte, zugklappte
Menüs) — deshalb ist `hidden_text` nur `medium` und der Scan abschaltbar.

## Grenzen (dokumentiert)

- CSS-Auswertung ist eine Heuristik, kein vollständiger Cascade: inline
  Styles und einfache `<style>`-Regeln (ID/Klasse/Element/Descendant)
  werden berücksichtigt; `@media`, komplexe Selektoren und Stylesheets von
  Fremd-Origins nicht.
- Farbvergleich: Hex und eine kleine Named-Color-Menge; `rgb()/hsl()` und
  vererbte Hintergründe werden nicht aufgelöst.
- Der Scan läuft im Daemon auf dem Snapshot-Erfassungspfad (`internal/daemon/capture_frames.go`), sodass Snapshot-Baum und Warnungen in einem einzigen Frame zurückgegeben werden und MCP-Tools wie CLI-Aufrufe die Warnungen direkt erhalten.

## Fetch-Pfad

`fetch_url` und `fetch_batch` scannen das originale HTML im Daemon mit demselben
bounded Scanner. `fetch_url` liefert `warnings[{kind, severity, ref, excerpt}]`
als Frame-Warnungen; `fetch_batch` legt die Warnungen beim jeweiligen URL-Eintrag
ab, damit ein Treffer nicht der falschen Seite zugerechnet wird. Der Inhalt wird
nicht verändert.

Der MCP-Daemon aktiviert `content_boundaries` standardmäßig. Strukturierte
Antworten tragen `content_boundaries` als separates Boundary-Objekt; Markdown
und Text setzen die Nonce-Marker um den Seitenkörper. Frontmatter und
Metadaten-Kopf bleiben außerhalb der Grenze. `no_injection_scan` deaktiviert
den Scan nur für den jeweiligen Aufruf und wird geloggt;
`injection_patterns` kann die eingebettete Musterliste durch eine Datei ersetzen.

## Scan budget and memoization

Snapshot injection scanning remains enabled by default. The daemon caps HTML
handed to `injection.Scan` at **1 MiB (1,048,576 bytes)**. When the page is
larger, only the bounded prefix is scanned and an `injection_scan` warning
states that content beyond the cap was not scanned. This is an explicit
security limitation, not a clean-scan result.

Results are memoized per page URL, tab/page, pattern-file path, and document
version. The document version is a hash of the accessibility snapshot tree
already captured by the snapshot command. A repeated snapshot of an unchanged
document skips both the `InspectHTML` round trip and the HTML parse. Navigation
or a DOM change changes the key and causes a fresh scan. The in-memory cache is
bounded to 128 entries and is discarded as a whole when that bound is reached.

## Local measurement

The review evidence used a synthetic 1.90 MB page with 20,000 DOM nodes and
reported approximately 40.7 ms for the first scan and 38.8 ms for a warm scan.
The regression tests verify the daemon-level property directly: two snapshots
of an unchanged fake document perform exactly one `InspectHTML` call; a changed
accessibility tree performs two.

A local Apple M4 Pro benchmark on this change used a larger synthetic nested DOM
and measured the direct scanner, not the daemon round trip:

| Input | `go test -bench ... -benchtime=3x` |
|---|---:|
| 1.9 MB full document | 8.09 s/op |
| 1 MiB capped prefix | 3.39 s/op |

The benchmark is intentionally not committed; the daemon regression tests are
the durable performance contract. Exact timings depend strongly on DOM shape and
should not be compared with the review fixture as if they were identical.

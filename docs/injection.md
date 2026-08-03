# Prompt-Injection-Scan

`snapshot` prüft den Seiteninhalt standardmäßig heuristisch auf
Prompt-Injection-Vektoren (ARCHITEKTUR.md §5.7, Issue #28). Das Ergebnis
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
- Der Scan läuft CLI-seitig auf dem Snapshot-Pfad. Die MCP-Tools erben den
  Scan, sobald die MCP- und die Injection-Welle gemergt sind (Follow-up auf
  dem MCP-Pfad).

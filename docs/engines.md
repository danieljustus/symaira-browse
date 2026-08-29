# Engines und die Engine-Grenze

`symbrowse` spricht nie direkt mit einem Browser, sondern immer über die
Engine-Grenze in [`internal/engine`](../internal/engine). Dieses Dokument hält
fest, welche Engines es gibt, welche Fähigkeiten eine Engine haben *muss* und
warum ein Safari-Backend anders behandelt wird als ein zweiter
Chromium-Browser.

## Was eine Engine ist

Die Grenze besteht aus einem Pflicht-Interface und derzeit 19 optionalen
Erweiterungen:

- `Engine` (Pflicht): Launch, Context, Page, Navigate, Evaluate, AXTree,
  Screenshot, Close.
- Optional, je nach Fähigkeit: `NetworkEvents`, `NetworkPolicyReporter`,
  `CookieEngine`, `FileTransfer`, `A11yAuditor`, `InteractionEngine`,
  `ClickDiagnosticEngine`, `TabManager`, `FrameManager`, `DialogController`,
  `ScriptDisabler`, `OverlayHost`, `RuntimeEvents` und weitere.

Eine Engine implementiert, was sie ehrlich kann. Was sie nicht kann, darf sie
**nicht** stillschweigend zu einem Teilergebnis degradieren — der Aufruf muss
als „von dieser Engine nicht unterstützt" scheitern.

## Bestand

| Engine | Protokoll | Status |
|---|---|---|
| `chrome` | CDP über WebSocket | vollständig; Referenzimplementierung |
| `static` | HTTP ohne Browser | bewusst reduziert (Tier 0) |
| `safari-attach` | Apple Events (`do JavaScript`) | Lesen vollständig; Navigation policy-guarded; Bedienen hinter Opt-in (issue #297) |

Die CDP-Engine ist nicht auf Google Chrome festgelegt. Die Discovery in
[`internal/engine/doctor`](../internal/engine/doctor) findet Chrome, Chromium
und Edge; `executable_path` bzw. `SYMBROWSE_EXECUTABLE_PATH` überschreibt sie.
„Chrome-only" ist damit ungenau — korrekt ist **CDP-only**.

## Warum kein zweiter Browser „einfach so"

Ein zusätzlicher Browser ist keine zusätzliche `Engine`-Implementierung,
sondern eine Fähigkeitsmatrix über bis zu 20 Interfaces. Zwei Engines dürfen
sich im *Umfang* unterscheiden. Sie dürfen sich nicht in der *Wahrheit*
unterscheiden: dieselbe Aktion darf nicht in der einen Engine korrekt scheitern
und in der anderen fälschlich gelingen.

## Safari über Apple Events

Safari hat kein CDP. `safaridriver` öffnet ein isoliertes Automationsfenster
ohne die Sitzung des Menschen. Der einzige Weg zur **laufenden, eingeloggten**
Safari-Sitzung führt über Apple Events (`do JavaScript`), was zwei
Voraussetzungen hat:

- Safari → Einstellungen → Erweitert → „Funktionen für Webentwickler anzeigen"
- Safari → Einstellungen → Entwickler → „JavaScript von Apple Events erlauben"
- dazu die macOS-Automation-Freigabe für den aufrufenden Prozess

### Gemessene Fähigkeiten

Gemessen am 2026-08-27 gegen die Fixtures aus
[`internal/testserver`](../internal/testserver), Safari 27.0
(22625.1.29.11.25), macOS 27.0. Kein geschätztes Ergebnis.

| Fähigkeit | Fixture | Ergebnis |
|---|---|---|
| Text füllen (Native-Setter + `input`) | `/form` | ✅ |
| Select, Checkbox, Radio | `/form` | ✅ |
| Klick-Handler auslösen | `/form` | ✅ (`isTrusted: false`) |
| User Activation, `window.open` | `/form` | ✅ |
| Shadow DOM (open) | `/shadow-dom` | ✅ |
| Verschachtelte same-origin iframes | `/iframe` | ✅ Kind und Enkel |
| Auf SPA-Hydration warten (Polling) | `/spa` | ✅ |
| `text`/`source` des Tabs ohne `do JavaScript` | beliebig | ✅ |
| Datei-Upload | `/form` | ❌ `InvalidStateError` |
| Cross-Origin-iframe | injiziert | ❌ `contentDocument === null` |
| CSS-`:hover` | injiziert | ❌ `matches(":hover") === false` |
| Console-Hook über eine Navigation hinweg | `/spa` → `/static` | ❌ verworfen |
| Netzwerk-Policy (Allowlist, SSRF) | — | ❌ keine Subresource-Schicht; Navigationsziele werden vor dem Apple Event geprüft |

Anders als zunächst angenommen trägt Apple-Events-JavaScript **User
Activation**. Aktivierungsgebundene APIs (Popups, Zwischenablage, Fullscreen,
WebAuthn) sind damit nicht kategorisch gesperrt.

### Der Befund, der die Entscheidung trägt

Auf `/overlay` verdeckt ein Modal den darunterliegenden Button:

```text
document.elementFromPoint(…) → "overlay-backdrop"   (ein Mensch trifft ihn nicht)
button.click()               → "underlying clicked" (der Handler feuert trotzdem)
```

JavaScript-`click()` umgeht das Hit-Testing. Eine naive Safari-Engine meldete
damit **Erfolg für eine Aktion, die ein Mensch nicht ausführen kann** — während
die CDP-Engine dieselbe Aktion korrekt als Interception meldet. Das ist kein
fehlendes Feature, sondern eine Abweichung in der Wahrheit, und es ist das
Hauptrisiko dieses Backends.

Zwei weitere Fallen, beide in der Messung aufgetreten:

- **Fenster-Ziel:** `current tab of window 1` zeigt auf das Fenster, das der
  Mensch gerade vorne hat, nicht auf das der Sitzung. Eine Engine darf
  ausschließlich einen fixierten Tab adressieren.
- **Kein Navigations-Lifecycle:** unmittelbar nach `set URL` liefert die Seite
  noch den **alten** Pfad mit `readyState: "complete"`. Es gibt kein
  Lade-Ereignis; es muss auf den URL-Wechsel gepollt werden, nicht auf
  `readyState`.

## Entscheidung

1. Ein Safari-Backend wird gebaut, aber als eigene Engine `safari-attach`, die
   sich an eine **laufende** Sitzung hängt statt einen Prozess zu starten.
2. Lesen zuerst, Bedienen danach — nicht weil Bedienen unmöglich wäre
   (es ist möglich), sondern weil der Schreibpfad die Fähigkeitsaushandlung
   voraussetzt.
3. Jeder Klick prüft vor der Ausführung per `elementFromPoint`, ob das Ziel
   tatsächlich getroffen würde, und meldet bei Verdeckung denselben Fehler wie
   die CDP-Engine.
4. `safari-attach` ist im MCP-Modus **lesend** (read-only): `Evaluate` wird
   ohne Opt-in verweigert und beantwortet bei Opt-in nur die festen
   Inspektionsausdrücke `document.title` und `location.href`. Navigationen
   akzeptieren ausschließlich HTTP(S) und passieren Allowlist und SSRF-Guard
   vor dem Apple Event. Die `InteractionEngine` erscheint nur bei Opt-in und
   aktivem URL-Guard; die Capability-Meldung (`doctor` und `engine info`)
   spiegelt das pro Modus ehrlich.
5. Jede Aktion in dieser Engine läuft in der echten, authentifizierten Sitzung
   des Menschen und erhält deshalb die höchste Risikoklasse.

## Nicht-Ziele

- Playwright oder eine Node-Laufzeit als Abhängigkeit. Verstößt gegen die
  standalone-first- und CGO-freie Invariante aus [AGENTS.md](../AGENTS.md).
- `safaridriver` als Backend: liefert eine isolierte Sitzung ohne die Logins
  des Menschen und damit genau den Nutzen nicht, um dessentwillen Safari
  überhaupt interessant ist.
- Getrennte Downloads oder Build-Tags pro Engine. `symbrowse` liefert keine
  Browser aus, sondern benutzt installierte; die Modularität gehört in die
  Fähigkeitsaushandlung und in `doctor`, nicht in die Release-Matrix.

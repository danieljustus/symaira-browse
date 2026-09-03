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
| `safari-bidi` | WebDriver BiDi über WebSocket (`safaridriver --bidi`) | Lesen in isolierter Sitzung; **kein** Bedienen, **keine** Events, **keine** Subresource-Policy — Safari implementiert die nötigen Module nicht (issue #355) |

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


## Safari über WebDriver BiDi

`safaridriver --bidi` öffnet eine **isolierte** Automationssitzung: die Tabs,
Cookies und Logins des Menschen sind nicht darin. `safari-bidi` ersetzt
`safari-attach` deshalb nicht, sondern steht daneben — der eine Weg führt zur
eingeloggten Sitzung, der andere zu einer sauberen.

### Zwei Voraussetzungen, die Apple nicht meldet

Die Sitzungserstellung scheitert nach 30 Sekunden mit *einem* Text für
*mehrere* Ursachen:

```text
Could not create a session: The session timed out while connecting to a Safari
instance. … Request creation of a new automation session
```

Die Unterscheidung steht ausschließlich im Unified Log, nie in der
WebDriver-Antwort:

```text
[com.apple.Safari:Automation] Rejecting session (…): Safari was not launched
for automation.
```

Das ist der Fall, in dem **Safari bereits normal läuft**: `safaridriver` hängt
sich an diese Instanz statt eine eigene zu starten, und Safari lehnt ab.
Gemessen am 2026-09-03: mit laufendem Safari scheitert die Sitzung, nach
`Cmd-Q` gelingt dieselbe Anfrage. Die zweite Ursache ist die fehlende
Automationsfreigabe (`sudo safaridriver --enable`). `doctor` erkennt beide und
nennt die jeweilige Abhilfe, statt Apples Timeout weiterzureichen.

### Zwei Fallen im Transport

- **Die Capability `webSocketUrl: true` genügt nicht.** Safari akzeptiert sie,
  spiegelt sie als Boolean zurück, setzt `safari:experimentalWebSocketUrl` auf
  `false` und öffnet **keinen** Socket. Erst
  `safari:experimentalWebSocketUrl: true` liefert eine echte `ws://`-URL.
- **Safari wählt den BiDi-Port selbst und prüft nicht, ob er frei ist.** Der
  `--bidi`-Parameter bestimmt ihn nicht; gemessen wurden für drei aufeinander
  folgende Sitzungen 8087, 8081 und 8094. In einer Messung zeigte die gemeldete
  URL auf Port 8081, den ein fremder SSH-Tunnel hielt. Die Engine verifiziert
  jeden Socket deshalb mit `session.status`, bevor sie ein Kommando sendet, und
  akzeptiert ausschließlich Loopback-Adressen.

### Gemessene Fähigkeiten

Gemessen am 2026-09-03 gegen die Fixtures aus
[`internal/testserver`](../internal/testserver), Safari 27.0 (26A5425a),
macOS 27.0, mit der Enumeration in
[`probe_surface_test.go`](../internal/engine/safaribidi/probe_surface_test.go).
Kein geschätztes Ergebnis.

| BiDi-Modul | Ergebnis |
|---|---|
| `session.*` (status, subscribe, unsubscribe) | ✅ Kommandos vorhanden |
| `browsingContext` navigate, getTree, create, close, activate, reload, traverseHistory, setViewport | ✅ |
| `script.*` (evaluate, callFunction, getRealms, addPreloadScript, disown) | ✅ |
| `browsingContext.captureScreenshot`, `locateNodes`, `print` | ❌ *was not found* |
| **`input.*`** (performActions, releaseActions, setFiles) | ❌ *'input' domain was not found* |
| **`network.*`** (addIntercept, continueRequest) | ❌ *'network' domain was not found* |
| `emulation.*`, `permissions.*`, `webExtension.*`, `bluetooth.*` | ❌ Domain fehlt |
| `storage.*` (getCookies, deleteCookies) | ⚠️ vorhanden, antwortet `InternalError` |
| **Event-Zustellung** (`browsingContext.load`, `log.entryAdded`, `network.*`) | ❌ `session.subscribe` meldet Erfolg und liefert **nichts** |

| Fähigkeit | Fixture | Ergebnis |
|---|---|---|
| Navigation mit echter Ladebarriere (`wait: "complete"`) | `/static` | ✅ |
| Gerenderte, hydrierte DOM lesen | `/spa` | ✅ (Unterschied zu `static`) |
| Verschachtelte iframes als eigene Browsing Contexts | `/iframe` | ✅ Kind und Enkel adressierbar |
| Titel, URL, Text, Attribute, Box, Styles | `/static`, `/form` | ✅ über `script.evaluate` |
| Klick mit echtem Hit-Testing | `/overlay` | ❌ kein `input`-Modul |
| Datei-Upload | `/form` | ❌ kein `input`-Modul |
| Screenshot | beliebig | ❌ Kommando fehlt |
| Console/Errors | `/spa` | ❌ keine Events |
| HTTP-Status, Network-Idle | beliebig | ❌ kein `network`-Modul |
| Netzwerk-Policy auf Subresources | — | ❌ kein `network`-Modul; nur Navigationsziele |

### Der Befund, der issue #355 umgeschrieben hat

Issue #355 begründete diese Engine mit einer Tabelle: BiDi sollte genau die
❌-Zeilen von `safari-attach` schließen — echtes Pointer-Input statt
`click()`, ein Navigations-Lifecycle, `log.entryAdded`, eine Netzwerkschicht
für Allowlist und SSRF, `input.setFiles`. Die Messung widerlegt **fünf der
sechs** Zeilen: Safari 27.0 liefert weder das `input`- noch das
`network`-Modul, und Events werden trotz erfolgreichem `subscribe` nicht
zugestellt. Adressierbare iframes bleiben als einzige Zeile bestehen.

Die Konsequenz für den Klick auf `/overlay` ist die wichtigste. Ohne
`input`-Modul bliebe nur ein JavaScript-`click()` — also genau der Weg, der
das Hit-Testing umgeht und in `safari-attach` **Erfolg für eine Aktion meldet,
die ein Mensch nicht ausführen kann**. `safari-bidi` implementiert deshalb
`InteractionEngine` **nicht** und meldet die Fähigkeit als nicht unterstützt,
statt sie unwahr zu erfüllen.

Ebenso ist die Netzwerk-Policy nicht weiter als in `safari-attach`: Allowlist
und SSRF-Guard prüfen Navigationsziele, Subresources bleiben ungeprüft. Die
Engine sagt das beim Start über `NetworkPolicyReporter.Limitations()`, statt
eine Durchsetzung zu suggerieren, die es nicht gibt.

### Fähigkeitsmatrix der Engine

`safari-bidi` implementiert `InspectionEngine`, `NavigationStateProvider`,
`NetworkPolicyReporter`, `FrameManager` und `TabManager`. Alles andere meldet
es als nicht unterstützt. `AXTree` und `Screenshot` gehören zum
Pflicht-Interface: `AXTree` wird aus der **gerenderten** DOM synthetisiert (wie
in `static`, aber nach Skriptausführung), `Screenshot` scheitert typisiert.

### Risikoklasse

Unter `safari-attach`, über `static`: die Sitzung ist isoliert und trägt keine
Logins, deshalb braucht arbiträres `Evaluate` hier kein Opt-in. Sie startet
aber einen echten Browser mit echtem Netzwerkzugang, was `static` nicht tut.

### Wann diese Messung neu zu machen ist

[`probe_surface_test.go`](../internal/engine/safaribidi/probe_surface_test.go)
enumeriert die Kommando-Oberfläche und die Event-Zustellung gegen ein echtes
Safari:

```text
SYMBROWSE_SAFARI_BIDI=1 go test ./internal/engine/safaribidi -run ProbeSurface -v
```

Wandert ein Kommando von ABSENT nach PRESENT, darf die Engine die zugehörige
Fähigkeit melden — vorher nicht. Die Live-Tests in `live_test.go` schlagen
absichtlich fehl, wenn Safari plötzlich einen HTTP-Status liefert: eine
stillschweigende Verbesserung hinter einer unveränderten Matrix wäre genau die
Art Abweichung, die dieses Dokument verhindern soll.

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
- **`safaridriver --mcp` als Backend:** Der Safari-MCP-Server liefert eine
  isolierte Sitzung ohne die Logins des Menschen. Außerdem müsste symbrowse
  als MCP-Client LLM-förmige `content`-Blobs in seine stabile Engine- und
  Output-Grenze übersetzen. Das ist eine bewusste Schichtungsschranke, keine
  fehlende Funktion. Safari 27.0 auf macOS 27.0 lieferte bei der Messung am
  2026-09-02 für die laufende Automationssitzung `list_tabs: []`; die Sitzung
  ist daher auch inhaltlich nicht der eingeloggte Safari des Menschen.
- **`safaridriver --bidi` als Backend:** kein Nicht-Ziel mehr — als Engine
  `safari-bidi` gebaut (#355). Ihr Umfang ist durch die Messung oben bestimmt
  und deutlich kleiner als bei der Planung angenommen.
- **Plain WebDriver als Backend:** Auch die ursprüngliche WebDriver-Variante
  öffnet eine isolierte Sitzung ohne die Logins des Menschen und liefert damit
  nicht den Grundnutzen, für den Safari in symbrowse interessant ist.
- Apples Safari-MCP-Server ist ein legitimes Schwesterwerkzeug, das ein
  Entwickler direkt ausführen kann. Dass symbrowse es nicht als Backend
  konsumiert, ist deshalb keine Lücke im direkten MCP-Werkzeug.
- Getrennte Downloads oder Build-Tags pro Engine. `symbrowse` liefert keine
  Browser aus, sondern benutzt installierte; die Modularität gehört in die
  Fähigkeitsaushandlung und in `doctor`, nicht in die Release-Matrix.

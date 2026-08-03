# Domain-Allowlist-Netzwerk-Policy

`symbrowse` kann den Netzwerk-Layer auf eine Domain-Allowlist einschränken.
Aktiviert wird sie über `--allowed-domains`, `SYMBROWSE_ALLOWED_DOMAINS` oder
`allowed_domains` in `config.toml`:

```toml
# ~/.config/symbrowse/config.toml
allowed_domains = ["example.com", "*.example.com"]
```

```bash
symbrowse daemon --allowed-domains "example.com,*.example.com"
```

## Semantik

- **Deny-by-default:** Ist eine Allowlist konfiguriert, wird jede Anfrage
  blockiert, deren Host nicht zu einem Muster passt. Ohne Allowlist (kein
  Muster) ist das Verhalten unverändert offen.
- **Muster:** Bare Hostnamen, optional mit führendem `*.` (deckt Apex und
  alle Subdomains ab: `*.example.com` matcht `example.com`,
  `www.example.com`, `a.b.example.com`). Schemata, Ports, Pfade, Userinfo
  und nackte `*` werden abgelehnt, damit ein Tippfehler die Policy nicht
  stillschweigend aufweicht.
- **Erlaubte Schemata:** `http`, `https`, `ws`, `wss`. Alles andere
  (`file:`, `data:`, `javascript:`, `chrome:`, `blob:`, …) ist blockiert,
  solange die Policy aktiv ist.
- **WebRTC** wird prozessseitig deaktiviert, solange die Policy aktiv ist
  (WebRTC ist kein HTTP-Request und über die Fetch-Interception nicht
  abbildbar).

## Was abgedeckt ist

Die Policy greift unterhalb von Seiten-JavaScript auf CDP-Ebene
(`Fetch.enable` mit Catch-all-Interception im Request-Stadium):

- Navigationen (inkl. `<meta refresh>`, `window.open`, Link-Klicks und
  jeden Redirect-Hop) — zusätzlich mit Fail-Fast-Gate in `Engine.Navigate`,
  damit eine blockierte Navigation nicht erst den Browser startet.
- Subresources (Bilder, Skripte, Styles, Fonts, XHR/Fetch, …)
- WebSocket-Handshakes, EventSource, `sendBeacon`
- Worker-Bootstrap (dedicated/shared/service worker): verwandte Targets
  werden per `Target.setAutoAttach` angehängt und erhalten dieselbe Policy.

Blockierte Anfragen werden pro URL gezählt und auf jeder Daemon-Antwort als
`warnings[]` gemeldet (`kind: network_policy`, `network_policy.blocked`).

## Bekannte Einschränkungen

- **Wiederverwendetes Chrome-Profil:** Läuft bereits ein Chrome-Prozess
  gegen das konfigurierte Profil (`SingletonLock`/`SingletonSocket`
  vorhanden), kann die Allowlist nicht garantiert werden — die Requests des
  fremden Prozesses liegen außerhalb unserer Interception. Beim Start wird
  dann `network_policy.limitation` gewarnt. Für garantierte Durchsetzung ein
  privates Profil verwenden.
- **Auto-Connect-Modus:** Ein künftiger Modus, der sich mit einem laufenden
  Chrome verbindet, hat dieselbe Einschränkung und wird beim Start gewarnt.
- Die Policy ist eine Browser-seitige Sperre; sie ersetzt keinen
  Proxy/Filter auf Host-Ebene.

## Zusammenspiel mit dem SSRF-Guard

Der SSRF-Guard (RFC1918/Loopback/Link-Local/`.local`/IPv6-ULA, siehe
[docs/ssrf.md](ssrf.md)) ist eine zweite, unabhängige Policy. Beide können
gleichzeitig aktiv sein; eine Anfrage muss von beiden erlaubt werden.

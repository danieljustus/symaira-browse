# Fehlercodes (Error Codes)

Alle symbrowse-Ausgaben folgen einem einheitlichen Schema. Mit dem globalen
`--json` Flag liefert jedes Kommando genau ein Envelope-Dokument:

Erfolg:

```json
{"success":true,"data":…,"warnings":[…]}
```

Fehler:

```json
{"success":false,"error":{"code":"stale_ref","message":…,"hint":…,"details":{…}}}
```

`code` ist immer ein Mitglied des unten dokumentierten Enums — niemals ein
freier String. Der Prozess-Exit-Code folgt weiterhin der `corekit/exitcodes`
Konvention (`internal/exitcodes`); die Zuordnung `code → exit code` steht in
`internal/output/codes.go` (`ExitCodeFromCode`).

## Enum

| Code | Bedeutung | Exit | Kind |
|---|---|---|---|
| `stale_ref` | Zugriff auf eine Ref, die aus der Seite verschwunden ist (Tombstone) | 6 conflict | conflict |
| `unknown_ref` | Ref ist nicht in der aktuellen Ref-Map | 5 not_found | not_found |
| `invalid_args` | Kommando-Argumente fehlen oder sind unbrauchbar | 2 no_input | validation |
| `invalid_inspection` | Nicht unterstützte Inspection-Art oder -Nutzung | 2 no_input | validation |
| `malformed_request` | Daemon-Frame konnte nicht dekodiert werden | 2 no_input | validation |
| `unknown_command` | Daemon kennt das Kommando nicht | 2 no_input | validation |
| `operation_failed` | Daemon-Handler ohne spezifischeren Code fehlgeschlagen | 1 generic | unavailable |
| `operation_timeout` | Daemon-Operation überschritt ihr Timeout | 1 generic | unavailable |
| `peer_denied` | Verbindender Peer wurde abgelehnt | 4 forbidden | permission |
| `daemon_unavailable` | Daemon nicht erreichbar oder nicht startbar | 1 generic | unavailable |
| `invalid_session` | Session-Name ungültig oder unbekannt | 2 no_input | validation |
| `session_not_found` | Session existiert nicht | 5 not_found | not_found |
| `not_found` | Angeforderte Ressource existiert nicht | 5 not_found | not_found |
| `auth` | Authentifizierung fehlgeschlagen | 3 no_auth | auth |
| `permission` | Operation nicht erlaubt | 4 forbidden | permission |
| `validation` | Ein Wert besteht die Validierung nicht | 2 no_input | validation |
| `no_input` | Benötigte Eingabe fehlt | 2 no_input | validation |
| `config` | Konfiguration ungültig oder nicht lesbar | 9 config | config |
| `conflict` | Operation kollidiert mit dem aktuellen Zustand | 6 conflict | conflict |
| `unavailable` | Benötigter Dienst oder Ressource nicht verfügbar | 1 generic | unavailable |
| `internal` | Unerwarteter interner Fehler (Fallback) | 7 software | internal |

## Regeln

1. Jeder Fehlerpfad liefert einen Code aus diesem Enum (`output.IsValid`).
2. Fehler aus dem Daemon-Protokoll behalten ihren stabilen Code (`daemon.Error`).
3. `internal` ist der dokumentierte Fallback für nicht klassifizierte Fehler —
   es ist selbst ein Enum-Mitglied, kein freier String.
4. `details` (optional) trägt maschinenlesbaren Zusatzkontext.

# Ausgabeschema (Output Schema)

`symbrowse read` liefert dasselbe Ausgabeschema wie `symfetch`
(siehe [tiers.md](tiers.md) — ein Agent soll nur ein Schema lernen).

## Quelle des Schemas

Das Schema liegt seit der Repo-Konsolidierung im Code dieses Repos, nicht
mehr als kopierte Datei:

- [`internal/fetch/agentdom/document.go`](../internal/fetch/agentdom/document.go)
  — JSON-Feldnamen des `Document`-Typs
- [`internal/fetch/render/frontmatter.go`](../internal/fetch/render/frontmatter.go)
  — YAML-Frontmatter-Schlüssel

Der Konformitätstest ist
[`cmd/symbrowse/read_conformance_test.go`](../cmd/symbrowse/read_conformance_test.go)
(`TestHermeticReadConformance`). Er vergleicht hermetisch gegen eine
statische Fixture, **was beide Wege tatsächlich zurückgeben** — die
`fetch.url`-Frame-Antwort gegen den `read`-Pfad —, nicht was die Renderer
könnten. Genau das ist der Punkt: Ein Test, der `render.GenerateFrontmatter`
direkt aufruft, besteht auch dann, wenn kein Aufrufer die Funktion
verdrahtet.

Geprüft werden Frontmatter-Schlüssel, deren Werte (`title`, `lang`,
`schema_type`, RFC3339-`fetched_at`), die JSON-Feldnamen und der
Markdown-Körper beider Seiten.

## Frontmatter-Schlüssel (Markdown-Modus)

```text
---
title: …
url: …
fetched_at: …        # RFC3339 UTC
lang: …
tokens_est: …         # Zeichen / 4
schema_type: …        # JSON-LD @type, optional
---
```

Identisch zu symfetch: `title`, `url`, `final_url`, `fetched_at`, `lang`,
`tokens_est`, `schema_type` (optional: `source`, `snapshot_at` für
Wayback-Quellen; browse setzt sie nicht).

## Frontmatter bei `fetch_url`

`fetch_url` liefert denselben Block, ist aber **opt-in**: `frontmatter: true`.
Ohne die Option beginnt die Antwort direkt mit dem Inhalt. Der Block zählt
gegen `max_chars` (siehe [mcp.md](mcp.md#ausgabegrenzen)), wird also nicht
zusätzlich zum Budget ausgegeben.

## Fetch-Sicherheitsfelder

`fetch_url` und `fetch_batch` erweitern die SymFetch-Antworten optional um
`warnings` und `content_boundaries`. `warnings` folgen der Snapshot-Form
`{kind, severity, ref, excerpt}`; bei Batches stehen sie am jeweiligen
URL-Eintrag. Das Boundary-Objekt enthält `nonce`, `origin`, `start` und `end`.
In strukturierten Antworten bleibt es ein separates Feld, während Markdown
und Text den Seitenkörper zwischen `start` und `end` setzen. Frontmatter und
der Metadaten-Kopf bleiben außerhalb der Grenze.

## Cache-Handles

`cache_id`-Werte mit dem Präfix `out_` verweisen auf vollständig gespeicherte
Antworten. `cache_get` liefert sie für MCP-Clients zurück; die CLI-Variante ist
`symbrowse cache get <id> [--range a-b]`. `store_full_text` verwendet denselben
Identifier und schreibt nicht mehr unter den archivierten `~/.cache/symfetch`-
Pfad.

## JSON-Modus (`read --json`)

```json
{
  "success": true,
  "data": {
    "url": "…",
    "title": "…",
    "lang": "…",
    "fetched_at": "…",
    "tokens_est": 123,
    "schema_type": "…",
    "markdown": "…",
    "truncated": false,
    "char_count": 500,
    "meta": {"status_code": 200, "truncated": false}
  }
}
```

Die Metadaten-Feldnamen (`url`, `title`, `lang`, `fetched_at`, `tokens_est`,
`schema_type`, `truncated`, `char_count`, `status_code`) sind die
symfetch-Feldnamen. `markdown`, `raw`, `outline` und `meta` sind die
dokumentierten browse-Erweiterungen (Inhalts-Träger).

## Truncate-Semantik

Wie symfetch gilt ein Zeichenbudget (Default 15 000 Zeichen, identisch zu
symfetch `char_limit`). Bei Überschreitung werden Kopf und Fuß des Inhalts
zurückgegeben, der mittlere Teil ist markiert:

```text
… [truncated: N runes omitted] …
```

`truncated: true` und `meta.truncated: true` signalisieren den Zustand. Das
Ablegen des Vollinhalts im Cache ist Teil des Token-Budget-Vorhabens (B-19)
und gehört nicht zu `read`.

## Engine-Capabilities (`engine.info`, Schema-Version 4)

Ab Schema-Version 4 meldet der Daemon-Frame `engine.info` den
Fähigkeits-Deskriptor der aktiven Session-Engine (Issue #295). Vor dem ersten
Launch der Session-Engine wird der geplante Engine gemeldet, danach der
tatsächlich laufende. Das Schema ist stabil und maschinenlesbar:

```json
{
  "success": true,
  "data": {
    "kind": "chrome",
    "launch_mode": "launch",
    "interfaces": ["A11yAuditor", "…", "TabManager"],
    "unsupported": []
  }
}
```

- `kind` — Engine-Implementierung (`chrome`, `static`, …).
- `launch_mode` — `launch` (eigener Browserprozess) oder `attach` (Verbindung
  zu einem bestehenden DevTools-Endpoint, Issue #296).
- `interfaces` — die implementierten optionalen Engine-Erweiterungen, aus der
  kanonischen Liste in `internal/engine` (`OptionalInterfaceNames`).
- `unsupported` — die bekannten optionalen Erweiterungen, die die Engine
  nicht unterstützt. Ein Aufruf einer nicht unterstützten Operation schlägt
  mit dem typisierten `UnsupportedOperationError` fehl — nie mit einem
  plausibel aussehenden Teilergebnis.

`symbrowse doctor` meldet denselben Deskriptor im Check `engine`, damit
Diagnose und Laufzeit nie über die Engine-Grenze auseinanderliegen.

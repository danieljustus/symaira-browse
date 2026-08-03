# Ausgabeschema (Output Schema)

`symbrowse read` liefert dasselbe Ausgabeschema wie `symfetch`
(ARCHITEKTUR.md §6.1 — ein Agent soll nur ein Schema lernen).

## Quelle des Schemas

Die maschinenprüfbare Kopie liegt in
`internal/render/testdata/fetch-schema.json` und wurde am 2026-08-03 aus
`https://github.com/danieljustus/symaira-fetch` übernommen:

- `internal/agentdom/document.go` — JSON-Feldnamen des `Document`-Typs
- `internal/render/frontmatter.go` — YAML-Frontmatter-Schlüssel

Der Konformitätstest (`internal/render/conformance_test.go`) prüft, dass
browse-emittierte Frontmatter-Schlüssel und JSON-Feldnamen eine Teilmenge
dieses Schemas sind. Bei Schema-Änderungen in symfetch wird die Kopie samt
Datumsangabe aktualisiert.

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

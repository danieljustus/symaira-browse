# Symaira Browse (`symbrowse`)

> **Der Browser, den ein Agent bedienen kann und ein Mensch jederzeit übernehmen darf — ohne dass die Sitzung abreißt.**

Status: **Planungsphase.** Es gibt noch keinen Code — nur den Bauplan.

---

## Was das wird

`symbrowse` steuert einen echten Chrome über das Chrome DevTools Protocol,
adressiert Seitenelemente über deterministische, LLM-freundliche Referenzen
(`@e7`), hält Sitzungen zwischen Aufrufen warm und hat eine **eingebaute
Übergabestelle an den Menschen** — für Login, 2FA, CAPTCHA und die Freigabe
riskanter Aktionen.

## Abgrenzung zu `symaira-fetch`

Die beiden Werkzeuge sind zwei Stufen derselben Aufgabe, kein Ersatz füreinander:

| | [`symfetch`](https://github.com/danieljustus/symaira-fetch) — Tier 0 | `symbrowse` — Tier 1 |
|---|---|---|
| JavaScript | nein | ja |
| Interaktion | nein | ja (klicken, tippen, absenden) |
| Kosten | ~100 ms, ~30 MB | ~1–3 s, ~300 MB |
| Braucht Chrome | nein | ja |
| Wofür | Inhalt lesen | Arbeitsabläufe erledigen |

Beide liefern **dasselbe Ausgabeschema**. Ein Agent lernt ein Format, nicht zwei.
`symfetch` verweist bei client-gerendertem Inhalt selbst auf `symbrowse`.

## Dokumente

- **[ARCHITEKTUR.md](ARCHITEKTUR.md)** — Idee, Architektur, Entscheidungsprotokoll,
  Abgrenzung zum Vorbild [vercel-labs/agent-browser](https://github.com/vercel-labs/agent-browser)
- **[PLANUNG.md](PLANUNG.md)** — Milestones v0.1.0 → v1.0.0, 64 Issues, Abhängigkeitsgraph

## Lizenz

Apache-2.0 — siehe [LICENSE](LICENSE).

---
key: insight-20260401-notify-snapshot-p2-staleness
tags: [escalation, notify, p2, ops-snapshot]
folder: insights
created: 2026-04-01T14:48:29-03:00
updated: 2026-04-01T14:48:29-03:00
links: []
---

---
Title: Notify — Snapshot 14:47 + P2 staleness
Timestamp: 2026-04-01T14:47:00-03:00
Score: 8/10
Action: notify
Reason: Sistema estable (CPU 60°C, RAM 47%, disco 48%) pero 5 proyectos P2 sin actualización 8–9 días.
Details:
- CPU 60°C, RAM 47% usada (4283MB libre), disco 48% (61GB libre)
- P2 stale: health-20260323 (9d), hn-submit-flow (8d), post-publish-hook-plan (8d), chango-hn-content-loop (8d), hn-candidate-backlog (8d)
- Última interacción: 16m (telegram)
Insights refs: reasoning-insight-2026-04-01-1447; insight-20260401-triage-stale-p2-and-heat
Tags: [escalation, notify, p2, ops-snapshot]
---

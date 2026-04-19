---
key: insight-20260401-1604-investigate-stale-p2
tags: []
folder: insights
created: 2026-04-01T16:05:21-03:00
updated: 2026-04-01T16:05:21-03:00
links: []
---

---
Title: Investigation — P2 staleness + system snapshot (16:04)
Timestamp: 2026-04-01T16:04:00-03:00
Tags: [investigation, p2, ops-snapshot]
---
Snapshot validated (sentinel 16:03):
- CPU: 56.8°C
- RAM: 44.3% usada (4489 MB libres de 8063)
- Disco: 53.6% usado (54.3 GB libres de 117)
- Alerts: none

Findings:
- Los 5 P2 señalados existen como proyectos/notas en memoria, pero NO hay tasks activas asociadas en el tracker (tasks=list vacío; búsquedas por nombre sin resultados). Esto explica la "falta de actualización": no hay elementos accionables trackeados.
- Proyectos en memoria relacionados:
  - project-health-20260323 (scan de repos, última fecha guardada 2026-03-23)
  - project-chango-hn-content-loop / content-distribution-loop (sin tasks)
- No hay "acciones pendientes de verificación" en folder actions.
- Cadencia: tema "P2 staleness" aparece 2+ veces en insights 2026-03-24 y hoy.

Hypothesis:
- Staleness es principalmente organizacional (decomposition/ritual), no técnico. Falta descomponer cada P2 en un next-step claro con due-date y recordatorio ligero.

Recommended next steps (no ejecutados aún):
1) Crear 1 task P2 por proyecto con un "primer paso mínimo" (due en 3 días):
   - health-20260323 → "Correr scan de repos y actualizar nota"
   - hn-submit-flow → "Documentar flujo con browse() + hn_login y guardar snippet"
   - post-publish-hook-plan → "Bajar spec v0.1 (gatillos, canales, límites)"
   - chango-hn-content-loop → "Listar 3 candidates de Chango Daily para HN con rationale"
   - hn-candidate-backlog → "Crear backlog inicial con 5 items y criterios de selección"
2) Agregar un checkpoint recurrente semanal P2-cadence (review 15 min, lunes 10:00 ART).
3) Para HN: mantener rate limit (≤1 post, ≤5 comments/h) y preparar un dry-run del submit flow.

Risks:
- Crear tareas sin confirmación podría saturar el tracker si el alcance no es el correcto.
- Hook post-publish podría enviar notificaciones duplicadas sin gating; diseñar dry-run primero.

Verification plan:
- Tras crear tasks: tasks list muestre 5 P2 con due <= +3d y status pending.
- Al completar: nuevas entradas en insights y/o projects con updates fechados.

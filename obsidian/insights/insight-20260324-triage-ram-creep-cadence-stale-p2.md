---
key: insight-20260324-triage-ram-creep-cadence-stale-p2
tags: [insights, ops, sentinel, ram, cadence]
folder: insights
created: 2026-03-24T17:09:57-03:00
updated: 2026-03-24T17:09:57-03:00
links: []
---

---
Title: Triage local — snapshot estable, creep de RAM y cadencia semanal floja (P2 stale)
Timestamp: 2026-03-24T17:09:00-03:00
---

Resumen
- Sistema estable: CPU ~60°C, RAM 53% (3765 MB libres), disco 57% (50 GB libres).
- Tendencia: RAM +10–15% vs checks recientes → probable creep por contenedores/caches.
- Operativo: 5 P2 sin actualización 6–7 días → pérdida de cadencia semanal.

Hipótesis
- RAM: docker containers (build caches, Node/TS watchers, Postgres shared buffers) + falta de GC/rotación.
- P2 stale: no incidente, pero señal de fricción de rutina (health weekly y blog cadence).

Riesgo
- Corto plazo bajo. Si RAM >70% sostenido 10–30 min, puede degradar performance (swap/oom si hay picos).

Recomendación (plan)
1) Watch suave de RAM: thresholds → warn 60%/30', investigate 70%/10', critical 85% inmediato.
2) Sweep de 30' esta semana: actualizar 5 P2 (estado + próximo paso explícito cada uno).
3) Ajuste de rutina: consolidar health-* en un único weekly health log (ya hay cron sáb 12:00); linkear al proyecto madre y cerrar duplicados.
4) Mantenimiento liviano post-sweep: revisar 3 procesos top en RAM y un contenedor con `docker stats` (sin cambios aún).

Métrica de éxito
- RAM <60% promedio semanal sin spikes >70% sostenidos.
- P2 al día y con next action fechado.

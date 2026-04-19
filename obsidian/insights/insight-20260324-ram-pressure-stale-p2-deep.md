---
key: insight-20260324-ram-pressure-stale-p2-deep
tags: [triage, ops, ram, cadence, p2, plan]
folder: insights
created: 2026-03-24T19:56:03-03:00
updated: 2026-03-24T19:56:03-03:00
links: []
---

---
Title: RAM pressure + P2 cadence stall — hypothesis and recovery plan
Timestamp: 2026-03-24T19:55:00-03:00
Tags: [triage, ops, ram, cadence, p2, plan]
---
Snapshot (sentinel 19:54)
- CPU: 55.7°C
- RAM: 72.5% usada (~2.2 GB libre)
- Disco: 62% (44 GB libre)

Signal
- RAM por encima de la banda reciente (45–55%) desde ~18:30 → presión sostenida.
- 5 P2 sin movimiento 6–7 días → cadencia semanal floja.

Hipótesis raíz (prob.)
1) Creep de memoria en servicios AI/containers (llama-server, modelos) + falta de limits.
2) Caches/logs no rotados (docker, app) empujando RSS.
3) Swap/zram subóptimo → presión directa en RAM.

Riesgos
- >85% RAM sostenida → riesgo de OOM killer (servicios críticos caen). Degradación de latencia.

Plan recomendado (72h)
A) Mitigación inmediata (15’)
- Capturar top de consumo (ps/top/htop) y reiniciar 1–2 servicios más pesados si no están en uso (llama-server, workers ociosos).
- Docker: `system prune -f` + `image prune -a -f` (seguro si no hay builds locales pendientes).
- Verificar swap/zram activo y tamaño.

B) Guardrails (hoy)
- Alerta soft: RAM >80% por 30’ → notificación única; >85% por 10’ → sugerir reinicio de servicio pesado.
- Límites: asignar memory limit a containers más voraces (p.ej., 1.5–2GB) y `restart: unless-stopped`.

C) Cadencia P2 (30’ bloque)
- goal-1k-mrr-90d-20260318: definir 1 experimento y 1 outreach esta semana; métrica: 1 lead calificado.
- health-20260302/09/16: consolidar en “health-weekly-20260324” con snapshot, riesgos y acción correctiva.
- blog-published-20260317: preparar 1 post corto (tema: "Qué aprendí afinando a Chango como agente"), publicar y notificar link.

Alternativa (si tiempo hoy <30’)
- Solo A+B ahora; C mañana a primera hora (20’).

Éxito = RAM <60% estable 24h; 3 P2 reactivados con siguiente paso explícito.

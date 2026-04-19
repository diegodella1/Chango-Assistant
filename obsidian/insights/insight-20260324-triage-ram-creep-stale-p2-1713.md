---
key: insight-20260324-triage-ram-creep-stale-p2-1713
tags: [triage, ops, ram, cadence, p2]
folder: insights
created: 2026-03-24T17:14:07-03:00
updated: 2026-03-24T17:14:07-03:00
links: []
---

---
Title: Triage local — snapshot estable; RAM creep + P2 con cadencia floja
Timestamp: 2026-03-24T17:13:00-03:00
Score: 7/10
Acción recomendada: investigate
Tags: [triage, ops, ram, cadence, p2]
---

Snapshot validado (sentinel 17:13):
- CPU: 56.2°C
- RAM: 52% usada (3872 MB libres)
- Disco: 56.8% usado (50.5 GB libres)

Qué noto
- Estable sin alertas.
- RAM con creep sostenido (+10–15% vs baseline reciente ~38–45%). No incidente: disponibilidad alta y sin OOM.
- Cadencia floja: 5 P2 sin actualizar hace 6–7 días.

Hipótesis
- Creep por contenedores/background jobs y cache acumulada. Riesgo bajo hoy, pero conviene acotar para evitar sorpresas.

Plan propuesto
1) Investigación liviana (15–20'): 
   - Top memoria procesos: ps aux --sort -rss | head -10
   - Docker: docker stats --no-stream y límites de memoria
   - Logs de OOM: journalctl -k | grep -i oom -n (esperado: ninguno)
   - Revisión rápida de contenedores con uptimes anómalos
2) Preventivo
   - Alerta suave: notificar si RAM >60% sostenido 30m (sin pager).
   - Limpieza ocasional: prune de cachés temporales cuando supere 60%.
3) Cadencia de P2
   - Barrido de 30' hoy: actualizar estado/next step de cada P2.

Definición de éxito
- RAM vuelve a ~45–50% promedio en 24–48h.
- Cada P2 con próximo paso explícito y fecha.

Siguientes pasos
- Confirmar si agendamos barrido 18:00–18:30 ART.
- Confirmar activación de alerta suave (>60%/30m) para implementarla.

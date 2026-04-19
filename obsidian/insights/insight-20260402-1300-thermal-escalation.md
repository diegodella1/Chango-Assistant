---
key: insight-20260402-1300-thermal-escalation
tags: [triage, thermal, p2, thresholds]
folder: insights
created: 2026-04-02T13:01:30-03:00
updated: 2026-04-02T13:01:30-03:00
links: []
---

---
Title: Investigation — thermal spike + P2 staleness (13:00)
Timestamp: 2026-04-02T13:00:00-03:00
Score: 8/10 (escalation)
---
Snapshot (sentinel 13:00)
- CPU: 79.9°C (↑ fuerte desde 59.5°C a las 12:02) — cerca del throttling (~80°C)
- RAM: 51.7% usada (3894 MB libres de 8063)
- Disco: 47.6% (61.3 GB libres de 117)
- Alerts: none

Estado de proyectos/tareas
- P2 sin actualización: health-20260323 (10d), hn-submit-flow (9d), post-publish-hook-plan (9d), chango-hn-content-loop (9d), hn-candidate-backlog (9d)
- Tareas activas (IN_PROGRESS):
  • Implementar evaluator de thresholds de health en sentinel (policy v1) — due 2026-04-04
  • Implementar webhook GH Actions → Telegram para posts publicados — due 2026-04-05

Evaluación
- Riesgo alto por temperatura cerca del umbral de throttling; sin alertas aún.
- Repetición del tema “definir thresholds + hook de notificación” → requiere cerrar v1.

Propuesta de policy v1 (thresholds)
- CPU: WARN ≥70°C sostenido 5 min; ALERT ≥80°C inmediato; CRITICAL ≥85°C.
- Histeresis: limpiar WARN al bajar a ≤67°C, ALERT a ≤77°C, CRITICAL a ≤82°C.
- RAM: WARN ≥75% por 10 min; ALERT ≥85% por 5 min; CRITICAL ≥92%.
- Disco: WARN ≥80%; ALERT ≥90%.

Plan del evaluator (sentinel)
- Buffer circular de 10 muestras (2 min c/u ≈ 20 min)
- State machine con hysteresis para evitar flapping
- Salida: status global (OK/WARN/ALERT/CRITICAL) + reasons detalladas en JSON
- Log de transiciones y hook opcional de mensajería (se integra con Telegram en v2)

Siguientes pasos inmediatos
- Notificar a Diego por temperatura 79.9°C (rozando 80°C)
- Agregar criterios de aceptación y pasos a ambos tasks
- Iniciar research breve sobre patrones GH Actions → Telegram

Verificación de acciones previas
- Learn jobs activos: “Raspberry Pi 5 system health thresholds and alert policy” y “Raspberry Pi 5 thermal management” → OK
- Tasks ya marcadas IN_PROGRESS → OK

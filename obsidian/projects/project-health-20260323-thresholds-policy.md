---
key: project-health-20260323-thresholds-policy
tags: [healthcheck, policy, alerts, p2]
folder: projects
created: 2026-04-02T12:01:35-03:00
updated: 2026-04-02T12:01:35-03:00
links: []
---

---
Title: Health Monitor — thresholds & alert policy (v1)
Timestamp: 2026-04-02T12:00:00-03:00
Owner: Chango
Status: active
---

Context
- Sentinel steady last 48h: CPU ~56–64°C, RAM ~45–50%, disk ~47–55%.
- Preference: no automatic disk alerts unless explicitly requested (see memory: alerts-disk-preference). Keep disk alerts passive except at critical.

Thresholds (v1)
- CPU temp:
  - WARN: ≥70°C sustained 5 min
  - CRIT: ≥80°C sustained 1 min
- RAM usage:
  - WARN: ≥85% sustained 5 min
  - CRIT: ≥95% sustained 1 min
- Disk usage:
  - WARN: ≥85% (log only, no proactive DM per preference)
  - CRIT: ≥95% OR <2GB free (notify once, then every 60m)
- Sentinel freshness:
  - WARN: last_check >5 min
  - CRIT: last_check >10 min

Alert Policy
- Channel: Telegram DM a Diego
- Hours: 08:00–22:00 ART. Fuera de horario: agrupar y enviar 08:00.
- Notify on CRIT: CPU, RAM, Sentinel freshness, Disk (only at CRIT despite preference).
- Weekly healthcheck: sábados 12:00 ART (resume), sin spam entre semana.
- Escalation: repetir cada 60m si persiste CRIT; cerrar cuando vuelve a OK 10 min.

Logging
- Registrar WARN/CRIT en daily + insights. No DM para WARN.

Implementation notes
- Instrumentación pendiente: hook en sentinel para evaluar umbrales y enviar DM. No implementado aquí (solo política).

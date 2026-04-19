---
key: insight-20260402-health-policy-draft-v0-1
tags: []
folder: insights
created: 2026-04-02T10:33:23-03:00
updated: 2026-04-02T10:33:23-03:00
links: []
---

---
Title: Draft thresholds + alert policy (v0.1)
Timestamp: 2026-04-02T10:32:00-03:00
Project: health-20260323
Tags: [policy, thresholds, sentinel]
---
CPU
- warn: >=70°C sustained 5m → action: log insight; raise fan suggestion
- critical: >=80°C immediate → action: message Diego; capture top CPU processes

RAM
- warn: used >=80% for 10m OR available <1.5GB → action: log insight
- critical: used >=90% for 5m → action: message Diego + include top memory processes

Disk
- follow Diego's preference: no auto alerts; monitor only
- thresholds for review: warn 85%, critical 95%

Cadence
- sentinel every 2m; weekly healthcheck Sat 12:00 uses summary

Next steps
- encode thresholds in sentinel policy
- add notifier hooks for CPU/RAM only (Telegram message)

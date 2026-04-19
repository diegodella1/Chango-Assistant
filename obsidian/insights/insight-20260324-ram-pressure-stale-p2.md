---
key: insight-20260324-ram-pressure-stale-p2
tags: [insight, ops, triage, ram, cadence]
folder: insights
created: 2026-03-24T19:02:41-03:00
updated: 2026-03-24T19:02:41-03:00
links: []
---

Title: RAM pressure + stale P2 cadence — triage and plan
Timestamp: 2026-03-24T19:02:00-03:00
Snapshot: CPU 56.8°C, RAM 87% (1.0GB free), Disk 61% (45.8GB free). Top memory: llama-server 27%, claude 4.7%, dockerd 4.9%.
Interpretation:
- System stable but close to OOM risk if a spike hits; main driver is llama-server footprint + background services.
- Parallel signal: several P2 projects stale 6–7 days → cadence drift.
Risks:
- OOM killer under load (model inference or Docker spikes) → service restarts, degraded responsiveness.
- Cadence drift reduces compounding progress on P2s.
Recommendations:
- P0 now (with approval): restart/scale down llama-server or unload large model to free 1–2GB; confirm swap/zram status; set soft alert if RAM >80% for 30m.
- P1 today: docker stats review + prune unused (images/containers); audit background services; cap model server RAM where possible.
- P2 this week: enable zram-swap (2–3GB) if not active; tune swappiness; document a lightweight resource budget.
- Cadence reset: 30’ block to update 5 P2s (status + next step + owner/date).

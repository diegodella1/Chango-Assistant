---
key: insight-20260304-latency-metrics
tags: [insight_for_diego, heartbeat, live_ops]
folder: insights
created: 2026-03-04T14:11:22-03:00
updated: 2026-03-04T14:11:22-03:00
links: []
---

Vendor go/no-go metric: track both p50 (typical UX) and p95 (SLO) for smoke tests. Decide on p95 budget for the critical step and ensure p50 also improves vs baseline. Beware mixed workloads; segment timings. Refs: oneuptime.com/blog/...p50-vs-p95, optyxstack.com/.../latency-distributions-in-practice

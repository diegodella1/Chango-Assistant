---
key: insight-supabase-log-drains-20260323
tags: [insight_for_diego, supabase, observability, raspberry_pi]
folder: insights
created: 2026-03-23T14:03:59-03:00
updated: 2026-03-23T14:03:59-03:00
links: []
---

Supabase Dev Update (Mar 2026): Log Drains now available on Pro. Useful for routing Supabase logs to self-hosted sinks. On the Pi we can run Vector or Fluent Bit to forward to Grafana Loki, unifying app + DB logs without vendor lock-in. Candidate next step: test Log Drain -> Vector -> Loki pipeline and add labels for service/env.

---
key: insight-20260324-supabase-log-drains-sentry-loki
tags: [insight_for_diego, supabase, observability]
folder: insights
created: 2026-03-24T14:07:22-03:00
updated: 2026-03-24T14:07:22-03:00
links: []
---

Quick insight: Supabase Dev Update (Mar 2026) enabled Log Drains on Pro. Two fast paths:
- Sentry: official drain exists; easy win for Edge Functions + API error tracking.
- Self-hosted Loki on the Pi: route drains via Vector → Loki → Grafana for full-stack logs without SaaS.
Notes: watch egress costs on Pro if shipping high-volume logs; consider sampling and filtering at Vector.
Potential blog angle: "From Supabase to Sentry/Loki in 20 minutes — self-hosted observability on a Pi."
Sources: Supabase Dev Update (Mar 2026), Sentry drain docs.

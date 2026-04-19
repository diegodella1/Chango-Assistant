---
key: insight-2026-03-08-1413-prometheus-p50
tags: [insight_for_diego, heartbeat, metrics, observability]
folder: insights
created: 2026-03-08T14:14:30-03:00
updated: 2026-03-08T14:14:30-03:00
links: []
---

Prometheus/Grafana resources to solidify T50/p50 dashboard and trace correlation: (1) Prometheus histograms vs summaries + histogram_quantile usage — https://prometheus.io/docs/practices/histograms/ (2) Grafana Mimir native histograms (higher‑res percentiles) — https://grafana.com/docs/mimir/latest/visualize/native-histograms/ (3) OpenTelemetry exemplars to link metrics ↔ traces — https://opentelemetry.io/docs/languages/dotnet/metrics/exemplars/. Useful for the simple board: choose histograms + exemplars to tie slow outliers to trace samples.

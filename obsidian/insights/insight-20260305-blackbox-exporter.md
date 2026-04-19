---
key: insight-20260305-blackbox-exporter
tags: [insight_for_diego, pilot_t50, observability]
folder: insights
created: 2026-03-05T14:11:00-03:00
updated: 2026-03-05T14:11:00-03:00
links: []
---

Para medir T50 y fiabilidad del paso crítico del piloto sin instrumentar la app, usar Prometheus Blackbox Exporter con probes HTTP a los endpoints del proveedor. Métricas útiles: http_duration_seconds (p50/p95), probe_http_duration_seconds{phase="connect|tls|processing"}, errores por intento. Alertas: p50 > umbral por N min o tasa de fallos > X%.
Links: GitHub https://github.com/prometheus/blackbox_exporter ; guía paso a paso https://omarghader.github.io/monitor-api-health-blackbox-prometheus/

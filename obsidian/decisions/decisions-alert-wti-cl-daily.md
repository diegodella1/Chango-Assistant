---
key: decisions-alert-wti-cl-daily
tags: [alerta, mercados, petróleo, wti, cl]
folder: decisions
created: 2026-03-26T12:47:04-03:00
updated: 2026-03-26T12:47:04-03:00
links: []
---

Alerta diaria creada: Precio del barril de crudo WTI (futuro CL, símbolo CL=F) en USD.
- Frecuencia: cada 24 horas desde ahora
- Entrega: el asistente hará la consulta y te enviará el precio (deliver:false en cron)
- Contenido previsto: precio spot/last, variación diaria absoluta y porcentual, hora de la cotización, fuente por defecto: Yahoo Finance (CL=F)
- Ajustable: horario específico, fuente alternativa (CME/ICE), incluir gráfico 7d/30d.

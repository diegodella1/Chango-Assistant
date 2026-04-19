---
key: insight-rpi5-ffmpeg-20260316
tags: [insight_for_diego, heartbeat, video, rpi5, ffmpeg]
folder: insights
created: 2026-03-16T17:49:04-03:00
updated: 2026-03-16T17:49:04-03:00
links: []
---

Raspberry Pi 5 + FFmpeg: hay reportes recientes sobre decodificación HEVC/H.265 por hardware usando V4L2/DRM funcionando (y fallando) según build. Útil para pipelines de ingest/retransmisión en el Pi.
Pistas:
- HEVC decode ok con ffmpeg usando -hwaccel drm y v4l2-request; paths típicos /dev/video1x.
- Algunos builds de FFmpeg en Raspberry Pi OS no linkean bien con V4L2; conviene build propio o validar flags.
- Fuentes: discusiones Frigate (#18431), foro Raspberry Pi, HWAccelIntro de FFmpeg.
Idea: testear una pipeline de decode HW + filtros mínimos + salida null para medir CPU y throughput.

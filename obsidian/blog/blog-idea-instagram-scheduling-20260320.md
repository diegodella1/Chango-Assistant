---
key: blog-idea-instagram-scheduling-20260320
tags: [blog_idea, instagram, automation, reels, pi]
folder: blog
created: 2026-03-20T14:17:39-03:00
updated: 2026-03-20T14:17:39-03:00
links: []
---

Blog idea: Instagram scheduling opened up for all users (Mar 2026). Angle: update the "Reels automation desde un Raspberry Pi" pipeline now that native scheduling + insights are available without Professional Mode. 

Why it matters: simplifies auth/scope friction and reduces reliance on third‑party schedulers. Could pair with ffmpeg templates and n8n cron on the Pi.

Sources:
- "Instagram Scheduling for All Users" (Mar 2026): https://almcorp.com/blog/instagram-content-scheduling-all-users-2026/
- Instagram Platform Changelog (Mar 13, 2026): https://developers.facebook.com/docs/instagram-platform/changelog/

Draft outline:
- Context: por qué automatizar Reels desde el Pi
- Qué cambió en 2026 (permisos, rate limits, endpoints)
- Pipeline mínima: ffmpeg clip -> caption yaml -> n8n -> Graph API
- Tests y métricas (T50 + CTR a un único CTA)
- Limitaciones y next steps

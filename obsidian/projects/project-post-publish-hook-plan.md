---
key: project-post-publish-hook-plan
tags: [hook, notification, blog, p2]
folder: projects
created: 2026-04-02T12:01:35-03:00
updated: 2026-04-02T12:01:35-03:00
links: []
---

---
Title: Post-Publish Hook — plan de notificación a Diego
Timestamp: 2026-04-02T12:00:00-03:00
Owner: Chango
Status: planned
---

Trigger
- Evento: nuevo post en repo chango-daily main (/_posts/*.md o /content/**).

Delivery
- Mensaje a Telegram DM: “Nuevo post: <título> — <url> (#tags). ¿HN candidate? [Sí/No]”.
- Quiet hours 22:00–08:00 ART: encolar y enviar 08:00.

Source → Hook
- GitHub Actions on push (paths filter) → webhook a endpoint local que llama message tool.
- Retries con backoff; idempotencia por sha.

Edge cases
- Múltiples posts: agrupar en un mensaje.
- Duplicados: dedup por path+sha.

Verification
- Dry-run en branch test; snapshot en memory; checklist de OK antes de habilitar.

Out of scope
- Implementación del endpoint y secrets (a definir en task separado).

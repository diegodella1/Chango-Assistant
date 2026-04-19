---
key: decision-event-pipeline-webhook-wrapper-spool
tags: [architecture, reliability, events]
folder: decisions
created: 2026-03-24T16:00:48-03:00
updated: 2026-03-24T16:00:48-03:00
links: []
---

Adopt a FastAPI /events webhook plus a Python wrapper with retries, backoff/jitter, and idempotency. Use a local SQLite spool for offline queueing and replay on startup to prevent dropped events.

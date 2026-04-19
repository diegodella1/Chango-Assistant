---
key: idea-blog-2026-03-04-curl-timing-smoke-test
tags: [blog_idea, ops, performance, tools]
folder: inbox
created: 2026-03-04T19:41:32-03:00
updated: 2026-03-04T19:41:32-03:00
links: []
---

Blog idea: Quick smoke test playbook using `curl -w` to capture time_namelookup, time_connect, time_starttransfer (TTFB) and time_total (TTLB), with a tiny bash snippet and acceptance thresholds (e.g., T50 TTFB < 300ms, error rate ~0%). Optional: use `oha` for a short burst to validate stability.

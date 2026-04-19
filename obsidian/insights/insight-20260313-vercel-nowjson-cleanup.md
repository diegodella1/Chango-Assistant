---
key: insight-20260313-vercel-nowjson-cleanup
tags: [insight_for_diego, devops, vercel, automation]
folder: insights
created: 2026-03-13T14:26:40-03:00
updated: 2026-03-13T14:26:40-03:00
links: []
---

Vercel will remove support for now.json on 2026-03-31. Quick hygiene win: scan repos for now.json and auto-PR rename to vercel.json (content unchanged). Edge cases: monorepos with multiple configs; consider moving advanced configs to vercel.ts later. Script idea: gh api search/code + sed rename; dry-run first.

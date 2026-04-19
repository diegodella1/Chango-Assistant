---
key: insight-supabase-pg-graphql-20260312
tags: [insight_for_diego, supabase, security]
folder: insights
created: 2026-03-12T14:15:17-03:00
updated: 2026-03-12T14:15:17-03:00
links: []
---

Supabase announced that in ~3 weeks pg_graphql will be disabled by default on new projects to reduce exposed API surface. For Diego’s stack (often PostgREST-first), this likely has low impact, but any new Supabase projects that relied on GraphQL will need explicit enablement or alternative (PostgREST). Security-positive change; worth noting for templates/boilerplates.

---
key: insight-20260402-1203-investigate-system-and-p2
tags: [investigation, p2, ops-snapshot, staleness, hn-loop]
folder: insights
created: 2026-04-02T12:04:10-03:00
updated: 2026-04-02T12:04:10-03:00
links: []
---

---
Title: Investigation — system snapshot + P2 staleness (12:03)
Timestamp: 2026-04-02T12:03:00-03:00
Score: 7/10 (triage escalation)
---
Snapshot validation (sentinel 12:02)
- CPU: 59.5°C
- RAM: 49.6% usada (4062 MB libres de 8063)
- Disco: 47.6% usado (61.3 GB libres de 117)
- Alerts: none

Findings
- Tasks tracker
  • P2 — health-20260323 → status: done (4975af2a5e2b)
  • P2 — hn-submit-flow → status: done (eaf995181f26)
  • P2 — post-publish-hook-plan → status: done (50390b5b589a)
  • P2 — chango-hn-content-loop → status: done (8ca863fdc211)
  • hn-candidate-backlog → no task found in tracker
- Memory vault
  • project-hn-candidate-backlog exists (topics list). It lives as a project note, not a task.
  • Multiple insights already logged today about P2 staleness and system snapshots.
- Actions pending verification → none found with tag 'pending-verification'.

Interpretation
- The "staleness" alert for 4 P2 items is outdated: all 4 are done as of ~12:00.
- The only mismatch is "hn-candidate-backlog": tracked as a project note (no task object), which likely causes recurring false positives in staleness scans that only look at tasks.
- Started background learn on "Hacker News submission and engagement loop" to consolidate best practices and feed the hn-* projects.

Risks
- Recurrent false positives due to split tracking (projects in memory vs tasks in tracker).

Proposed adjustments (not executed)
1) Create a P2 task: "hn-candidate-backlog — weekly refresh + pick top 3" and link it to the project note.
2) Harmonize staleness scan: include project notes with "project-*" keys and last-updated timestamp, or enforce a task stub for each project.
3) Keep hn-submit-flow v1 and content-loop criteria in one index note linking tasks + assets.

Next checks
- When learn job completes, append a summary insight and update the hn-* assets with concrete tactics.

---
key: post-publish-hook-plan
tags: [automation, blog]
folder: projects
created: 2026-03-24T04:00:39-03:00
updated: 2026-03-24T04:00:39-03:00
links: []
---

Blog post-publish hook plan:
Trigger: when gh api PUT to posts/YYYY-MM-DD-slug.md succeeds.
Actions:
- memory.save key="last_blog_post" with title/date/slug.
- message Diego with the post URL.
- If source idea exists, update it to [USED] with published date.
- Optional: generate shortlink and append to message.
Implementation options:
A) Wrap gh api in a small script that, on success, calls the bot webhook with {title, url, slug}.
B) If publishing via PR merge, use GitHub Actions to POST to bot webhook on path filter posts/**.
Recommendation: start with A, move to B later for reliability.

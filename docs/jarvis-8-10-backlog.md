# PicoClaw 8/10 Backlog

This document turns the high-level plan in `docs/jarvis-8-10-plan.md` into a concrete implementation backlog.

## How To Use This

- Treat each ticket as a GitHub issue candidate.
- Keep the implementation order unless a dependency clearly changes.
- Do not start Phase 3 or later while Phase 1 is still unstable in production.
- A phase is "done" only when its exit criteria are true in prod, not just merged.

## Priority Scale

- `P0`: blocks reliability or creates production instability
- `P1`: major leverage, should happen immediately after P0
- `P2`: important but can wait until foundations are stable

## Phase 1: Reliability And Provider Orchestration

Target outcome:
- provider failures stop feeling catastrophic
- `/health` explains degraded state
- repeated fallback loops are visible and bounded

### Ticket 1. Provider policy registry

- Priority: `P0`
- Goal: centralize provider routing rules by workload type
- Scope:
  - add a policy layer in `pkg/providers`
  - define workload classes: `chat`, `reasoning`, `background`, `vision`, `transcription`
  - for each class define preferred provider, fallback chain, timeout, max context, retry budget
- Primary files:
  - `pkg/providers/types.go`
  - `pkg/providers/privacy_router.go`
  - new file likely needed in `pkg/providers`, for example `policy.go`
- Done when:
  - routing is configuration-backed or policy-backed instead of implicit scattered logic
  - a single function can answer "what providers are allowed for this task?"

### Ticket 2. Provider telemetry expansion

- Priority: `P0`
- Goal: make provider behavior measurable
- Scope:
  - record success, failure, fallback, latency, and error type per provider
  - expose counters and recent failure streaks via telemetry APIs
- Primary files:
  - `pkg/telemetry/tracker.go`
  - `pkg/telemetry/budget.go`
  - `pkg/tools/telemetry.go`
- Done when:
  - a failing provider can be identified from telemetry without reading raw logs

### Ticket 3. Context pressure guardrails

- Priority: `P0`
- Goal: stop context-overflow failures before requests are sent
- Scope:
  - estimate prompt size before dispatch
  - summarize or trim oversized sections
  - add a degraded response mode when context pressure is high
- Primary files:
  - `pkg/agent/context.go`
  - `pkg/agent/loop.go`
  - `pkg/agent/summarize.go`
  - `pkg/providers/privacy_router.go`
- Done when:
  - large prompts degrade cleanly instead of producing provider `413` or context overflow errors

### Ticket 4. Health endpoint enrichment

- Priority: `P0`
- Goal: make `/health` operationally useful
- Scope:
  - include current provider route
  - include degraded mode status
  - include last successful provider/model
  - include recent provider failure summary
- Primary files:
  - `cmd/picoclaw/gateway.go`
  - `pkg/telemetry/tracker.go`
- Done when:
  - `/health` tells you whether the app is healthy but degraded, not only alive

### Ticket 5. Provider failure circuit breaker

- Priority: `P1`
- Goal: avoid endless retries on broken routes
- Scope:
  - detect repeated provider failures
  - temporarily suppress bad routes
  - auto-reopen after cooldown
- Primary files:
  - `pkg/providers/privacy_router.go`
  - `pkg/agent/loop.go`
- Done when:
  - one bad provider does not keep poisoning the same request path

### Phase 1 Exit Criteria

- [ ] `/health` exposes degraded mode and provider route info
- [ ] provider failures are visible in telemetry
- [ ] repeated route failures do not spiral indefinitely
- [ ] context overflow errors are materially reduced in production

## Phase 2: Structured State And Working Memory

Target outcome:
- PicoClaw knows what is active, blocked, due, and pending
- state survives restarts and deploys

### Ticket 6. Define structured state entities

- Priority: `P0`
- Goal: formalize operational state records
- Scope:
  - define durable entities for `task`, `project`, `blocker`, `reminder`, `decision`, `commitment`, `follow_up`
  - store lifecycle metadata, timestamps, owner/source, and current status
- Primary files:
  - `pkg/state/state.go`
  - `pkg/state/state_test.go`
  - possible new files under `pkg/state`
- Done when:
  - state records are typed and test-covered

### Ticket 7. Task and reminder persistence integration

- Priority: `P0`
- Goal: move task-like state onto the structured layer
- Scope:
  - wire `tasks`, `agenda`, and `reminder` tools to state storage
  - preserve backward-compatible behavior where possible
- Primary files:
  - `pkg/tools/tasks.go`
  - `pkg/tools/agenda.go`
  - `pkg/tools/reminder.go`
  - `pkg/tools/memory.go`
- Done when:
  - active tasks and reminders are queryable after restart

### Ticket 8. Active-state context assembler

- Priority: `P1`
- Goal: pull operational state into prompts intentionally
- Scope:
  - add retrieval rules for active tasks, blockers, due reminders, recent project changes
  - keep prompt assembly bounded and priority-driven
- Primary files:
  - `pkg/agent/context.go`
  - `pkg/agent/memory.go`
- Done when:
  - "what's pending?" can be answered from structured state, not fuzzy recall

### Ticket 9. Project state snapshot model

- Priority: `P1`
- Goal: track per-project state instead of spreading it across notes
- Scope:
  - add current goal, last change, next action, risk flags, and recent decisions for each project
- Primary files:
  - `pkg/state/*`
  - `pkg/tools/tasks.go`
  - `pkg/tools/knowledge_graph.go`
- Done when:
  - each tracked project has a compact current-state snapshot

### Phase 2 Exit Criteria

- [ ] tasks and reminders survive restarts
- [ ] blockers and next actions are persisted
- [ ] project status can be derived from structured state
- [ ] prompt assembly includes active state without exploding context

## Phase 3: Planner And Loop Closure

Target outcome:
- multi-step work has explicit states
- interrupted work resumes cleanly
- verification is part of execution, not an afterthought

### Ticket 10. Planner state machine

- Priority: `P0`
- Goal: formalize execution states
- Scope:
  - add states: `pending`, `in_progress`, `blocked`, `waiting_external`, `verification_failed`, `done`
  - unify state transitions across plan and agenda flows
- Primary files:
  - `pkg/agent/autonomy_plan.go`
  - `pkg/agent/autonomy_agenda.go`
  - `pkg/agent/autonomy_plan_test.go`
  - `pkg/agent/autonomy_agenda_test.go`
- Done when:
  - task execution state changes are explicit and test-covered

### Ticket 11. Verification hooks by action type

- Priority: `P0`
- Goal: require post-action validation
- Scope:
  - deploys require health/smoke checks
  - edits require build/test when available
  - outbound notifications require delivery or API confirmation when available
- Primary files:
  - `pkg/tools/toolloop.go`
  - `pkg/tools/workflow.go`
  - `pkg/tools/deploy.go`
  - `pkg/tools/edit.go`
  - `pkg/tools/message.go`
- Done when:
  - actions can land in `verification_failed` instead of pretending success

### Ticket 12. Follow-up scheduling for blocked work

- Priority: `P1`
- Goal: make incomplete work resumable
- Scope:
  - automatically create follow-up tasks when waiting on external input, cooldown, or review
  - schedule retries or reminders with explicit next-check timestamps
- Primary files:
  - `pkg/agent/autonomy_agenda.go`
  - `pkg/tools/tasks.go`
  - `pkg/tools/reminder.go`
- Done when:
  - blocked work does not vanish after a single attempt

### Ticket 13. Execution trace log

- Priority: `P1`
- Goal: make planner behavior inspectable
- Scope:
  - record objective, actions, intermediate results, verification, and final state
- Primary files:
  - `pkg/agent/autonomy_plan.go`
  - `pkg/tools/toolloop.go`
  - `pkg/state/*`
- Done when:
  - each long-running task has a readable execution trace

### Phase 3 Exit Criteria

- [ ] planner states are explicit
- [ ] verifications run after high-impact actions
- [ ] blocked work creates follow-up state
- [ ] long-running work leaves an execution trace

## Phase 4: Safe Autonomy And Policy Enforcement

Target outcome:
- PicoClaw can act on its own where appropriate
- risky actions remain bounded and reviewable

### Ticket 14. Tool action classification

- Priority: `P0`
- Goal: classify tools by operational risk
- Scope:
  - define action classes: `read_only`, `low_risk_write`, `sensitive_write`, `external_side_effect`, `destructive`
  - attach classifications to tools in the registry
- Primary files:
  - `pkg/tools/registry.go`
  - `pkg/tools/base.go`
- Done when:
  - every tool has a policy-visible risk class

### Ticket 15. Policy gate at execution boundary

- Priority: `P0`
- Goal: enforce approval rules centrally
- Scope:
  - before tool execution, check autonomy policy against tool class and context
  - support explicit approval presets in config
- Primary files:
  - `pkg/tools/registry.go`
  - `pkg/tools/self.go`
  - `pkg/config/config.go`
  - `config/config.example.json`
- Done when:
  - high-risk actions are blocked or require approval regardless of prompt wording

### Ticket 16. Autonomy audit log

- Priority: `P1`
- Goal: record why the system acted
- Scope:
  - log action, intent, trigger, risk class, approval path, result, and rollback notes
  - persist audit events
- Primary files:
  - `pkg/state/*`
  - `pkg/tools/self.go`
  - `pkg/tools/deploy.go`
  - `pkg/tools/git_tool.go`
  - `pkg/tools/host_exec.go`
- Done when:
  - autonomous actions can be reconstructed after the fact

### Ticket 17. Admin visibility for autonomy policy

- Priority: `P2`
- Goal: expose policy and recent actions in admin
- Scope:
  - add views for recent autonomous actions and denied actions
  - expose active autonomy presets
- Primary files:
  - `pkg/admin/admin.go`
  - `pkg/admin/settings.go`
  - likely new admin view files
- Done when:
  - policy behavior is visible without inspecting raw state files

### Phase 4 Exit Criteria

- [ ] tool risk classes exist
- [ ] policy gates are enforced centrally
- [ ] autonomous actions are audit-logged
- [ ] policy visibility exists in admin

## Phase 5: Proactive Briefings And Attention Quality

Target outcome:
- PicoClaw becomes useful before being asked
- proactive output is concise, timely, and grounded

### Ticket 18. Briefing generator pipeline

- Priority: `P1`
- Goal: generate structured briefings from operational signals
- Scope:
  - support `morning`, `incident`, `stale_project`, `overdue_task`, and `system_risk` briefings
- Primary files:
  - `pkg/heartbeat/service.go`
  - `pkg/attention/service.go`
  - `pkg/sentinel/service.go`
- Done when:
  - briefings are generated from state and telemetry, not improvised from raw chat history

### Ticket 19. Anti-noise notification rules

- Priority: `P1`
- Goal: reduce spam and repeated low-signal alerts
- Scope:
  - dedupe repeated alerts
  - throttle non-actionable messages
  - escalate only when next action is identifiable
- Primary files:
  - `pkg/attention/service.go`
  - `pkg/heartbeat/service.go`
  - `pkg/sentinel/service.go`
- Done when:
  - proactive messages feel actionable rather than chatty

### Ticket 20. Briefing history and explainability

- Priority: `P2`
- Goal: show what was sent and why
- Scope:
  - persist recent briefings and the signal inputs behind them
  - expose recent briefing history in admin
- Primary files:
  - `pkg/state/*`
  - `pkg/admin/*`
- Done when:
  - each briefing can be traced back to the signals that generated it

### Phase 5 Exit Criteria

- [ ] morning and incident briefings exist
- [ ] alert noise is materially reduced
- [ ] briefings can be inspected after the fact

## Phase 6: Production Observability And Deploy Confidence

Target outcome:
- production health is explainable from one place
- deploys verify themselves

### Ticket 21. Admin runtime dashboard

- Priority: `P1`
- Goal: make prod state visible at a glance
- Scope:
  - show provider route, degraded mode, failure streaks, pending tasks, blocked tasks, budget state
- Primary files:
  - `pkg/admin/admin.go`
  - `pkg/admin/logs.go`
  - likely new admin dashboard files
- Done when:
  - "what is wrong right now?" can be answered in one admin page

### Ticket 22. Sentinel and telemetry enrichment

- Priority: `P1`
- Goal: add the missing operational signals
- Scope:
  - include CPU temperature, memory pressure, task backlog growth, deploy success/failure, provider failure streak
- Primary files:
  - `pkg/sentinel/service.go`
  - `pkg/telemetry/tracker.go`
  - `pkg/heartbeat/service.go`
- Done when:
  - high-risk degradation patterns are visible before users complain

### Ticket 23. Deploy smoke test pipeline

- Priority: `P0`
- Goal: verify deploys automatically
- Scope:
  - after restart, check local health endpoint
  - verify expected port is listening
  - hit one representative public or proxied route when possible
  - fail loudly on mismatch
- Primary files:
  - `scripts/deploy_coolify_restart.sh`
  - `scripts/check_coolify_app.sh`
  - `scripts/check_coolify_deploy.sh`
- Done when:
  - a deploy is not considered successful until smoke checks pass

### Ticket 24. Version visibility in runtime

- Priority: `P2`
- Goal: know exactly what build is running
- Scope:
  - surface commit/version/build time in `/health` and admin
  - stop reporting only `version":"dev"` in production
- Primary files:
  - `cmd/picoclaw/main.go`
  - `cmd/picoclaw/util.go`
  - `cmd/picoclaw/gateway.go`
  - `Makefile`
  - `Dockerfile`
- Done when:
  - runtime reports the actual deployed commit and build metadata

### Phase 6 Exit Criteria

- [ ] deploys run smoke checks
- [ ] admin shows runtime state clearly
- [ ] health output includes real version metadata
- [ ] sentinel and telemetry cover the main failure modes

## Suggested GitHub Issue Ordering

Create issues in this order:

1. Ticket 1. Provider policy registry
2. Ticket 2. Provider telemetry expansion
3. Ticket 3. Context pressure guardrails
4. Ticket 4. Health endpoint enrichment
5. Ticket 6. Define structured state entities
6. Ticket 7. Task and reminder persistence integration
7. Ticket 8. Active-state context assembler
8. Ticket 10. Planner state machine
9. Ticket 11. Verification hooks by action type
10. Ticket 14. Tool action classification
11. Ticket 15. Policy gate at execution boundary
12. Ticket 23. Deploy smoke test pipeline
13. Ticket 21. Admin runtime dashboard
14. Ticket 18. Briefing generator pipeline

## Recommended Milestones

### Milestone 1. Runtime Reliability

- Tickets: 1, 2, 3, 4, 5

### Milestone 2. Working Memory

- Tickets: 6, 7, 8, 9

### Milestone 3. Loop Closure

- Tickets: 10, 11, 12, 13

### Milestone 4. Safe Autonomy

- Tickets: 14, 15, 16, 17

### Milestone 5. Proactive Operator

- Tickets: 18, 19, 20

### Milestone 6. Observable Production

- Tickets: 21, 22, 23, 24

## Fastest Path To Visible Improvement

If time is limited, the highest-leverage subset is:

- Ticket 1. Provider policy registry
- Ticket 2. Provider telemetry expansion
- Ticket 3. Context pressure guardrails
- Ticket 4. Health endpoint enrichment
- Ticket 6. Define structured state entities
- Ticket 7. Task and reminder persistence integration
- Ticket 10. Planner state machine
- Ticket 11. Verification hooks by action type
- Ticket 15. Policy gate at execution boundary
- Ticket 23. Deploy smoke test pipeline

That subset alone would move PicoClaw much closer to `8/10`.

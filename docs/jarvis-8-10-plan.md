# PicoClaw 8/10 Plan

## Objective

Move PicoClaw from a capable autonomous assistant to an `8/10` "Jarvis-like" system:

- reliable in production
- proactive without being noisy
- stateful across days and projects
- able to plan and close loops
- safe to trust with bounded autonomy

This is not a "more features" plan. The main gap is reliability, continuity, and execution discipline.

## Definition Of 8/10

At `8/10`, PicoClaw should:

- maintain structured state for projects, tasks, blockers, reminders, and personal context
- execute multi-step tasks with verification and follow-up
- survive provider failures without feeling broken
- produce useful daily and event-driven briefings
- act autonomously in low-risk domains with clear guardrails
- expose enough telemetry and admin visibility to debug production quickly

It does not need sci-fi-level generalized intelligence. It needs to feel dependable and operational.

## Current Gaps

Based on the current codebase and runtime shape, the main gaps are:

1. Provider reliability is still fragile.
   - Relevant modules: `pkg/providers`, `pkg/telemetry`, `pkg/agent/loop.go`
   - Symptoms already visible in prod logs: `429`, `402`, context overflow, local fallback instability.

2. Memory exists, but working state is not strong enough.
   - Relevant modules: `pkg/agent/memory.go`, `pkg/tools/memory.go`, `pkg/tools/tasks.go`, `pkg/tools/agenda.go`, `pkg/state`
   - The system needs durable structured objects, not only conversational recall.

3. Planning exists, but execution loops need hardening.
   - Relevant modules: `pkg/agent/autonomy_plan.go`, `pkg/agent/autonomy_agenda.go`, `pkg/tools/workflow.go`, `pkg/tools/toolloop.go`
   - The assistant should carry work from intent to done, not stop at good intermediate reasoning.

4. Autonomy lacks a stronger policy layer.
   - Relevant modules: `pkg/tools/self.go`, `pkg/tools/deploy.go`, `pkg/tools/git_tool.go`, `pkg/tools/host_exec.go`
   - We need explicit action classes, approval thresholds, auditability, and safe rollback posture.

5. Production observability is still too thin.
   - Relevant modules: `pkg/sentinel`, `pkg/heartbeat`, `pkg/telemetry`, `pkg/admin`
   - Health is present, but the system still needs better visibility into why behavior degraded.

## Workstreams

### 1. Reliability And Provider Orchestration

Goal: make the system resilient when one provider fails, slows down, or runs out of budget.

Implementation targets:

- Add a provider policy layer in `pkg/providers`.
  - Define ordered fallbacks by task class: chat, reasoning, background jobs, vision, transcription.
  - Encode max context size, timeout, retry policy, and budget class per provider.

- Expand telemetry in `pkg/telemetry/tracker.go`.
  - Track per-provider success rate, fallback rate, latency, token pressure, cost estimate, and error class.

- Add runtime safeguards in `pkg/agent/loop.go` and `pkg/providers/privacy_router.go`.
  - Trim or summarize oversized requests before they hit providers.
  - Detect repeated failure loops and degrade cleanly to a smaller response mode.

- Extend `/health` in `cmd/picoclaw/gateway.go`.
  - Include current provider state, last successful model route, pending failures, and degraded-mode status.

Success criteria:

- no silent provider death spirals
- graceful degradation instead of broken conversations
- enough telemetry to explain failure in under 5 minutes

### 2. Structured Memory And Working State

Goal: turn memory into a usable operational state system.

Implementation targets:

- Introduce durable state types for:
  - task
  - project
  - blocker
  - commitment
  - reminder
  - decision
  - follow-up

- Use `pkg/state` as the persistence layer for these records.
  - If `pkg/state` is too generic today, extend it rather than creating another persistence path.

- Tighten integration across:
  - `pkg/tools/tasks.go`
  - `pkg/tools/agenda.go`
  - `pkg/tools/reminder.go`
  - `pkg/tools/memory.go`
  - `pkg/agent/context.go`

- Add context assembly rules in `pkg/agent/context.go`.
  - Pull active tasks, current blockers, due reminders, and recent project events into prompts.
  - Stop relying only on broad historical memory retrieval.

Success criteria:

- user asks about a project and PicoClaw knows current status, next step, and open blockers
- reminders and commitments survive restarts and re-deploys
- the system can answer "what is pending?" from structure, not inference

### 3. Planner That Closes Loops

Goal: make autonomy concrete and outcome-oriented.

Implementation targets:

- Consolidate planning around a single execution lifecycle:
  - objective
  - plan
  - next action
  - execution
  - verification
  - follow-up
  - closure

- Strengthen:
  - `pkg/agent/autonomy_plan.go`
  - `pkg/agent/autonomy_agenda.go`
  - `pkg/tools/workflow.go`
  - `pkg/tools/toolloop.go`

- Add explicit result states:
  - pending
  - in_progress
  - blocked
  - waiting_external
  - verification_failed
  - done

- Add automatic follow-up generation.
  - If a task cannot complete now, PicoClaw should schedule or persist the next check.

- Add verification rules per tool/action category.
  - deploys require post-check
  - edits require build or test when available
  - outbound notifications require delivery confirmation when possible

Success criteria:

- fewer abandoned half-finished actions
- better handling of long-running work
- clear trace of what happened and what remains

### 4. Safe Autonomy Policy Layer

Goal: give the system bounded freedom without making it reckless.

Implementation targets:

- Add an action classification policy.
  - Read-only
  - Low-risk write
  - Sensitive write
  - External side-effect
  - Destructive

- Enforce policy at tool invocation boundaries in:
  - `pkg/tools/registry.go`
  - `pkg/tools/self.go`
  - `pkg/tools/deploy.go`
  - `pkg/tools/git_tool.go`
  - `pkg/tools/host_exec.go`

- Add a durable audit log.
  - Record action, intent, trigger, scope, result, and whether approval was required.
  - Expose it in `pkg/admin`.

- Add approval presets in config.
  - Example domains:
    - self-maintenance
    - project deploys
    - outbound messaging
    - file edits outside workspace

Success criteria:

- autonomous behavior becomes inspectable and predictable
- lower-risk actions can run without friction
- higher-risk actions never happen "by vibe"

### 5. Proactive Briefing And Attention System

Goal: make PicoClaw useful before being asked.

Implementation targets:

- Build briefing generators on top of:
  - `pkg/heartbeat/service.go`
  - `pkg/attention/service.go`
  - `pkg/sentinel/service.go`
  - `pkg/tools/tasks.go`
  - `pkg/tools/calendar.go`

- Create briefing types:
  - morning briefing
  - deploy/incident summary
  - stale project warning
  - overdue task digest
  - system risk summary

- Add anti-noise rules.
  - dedupe repeated alerts
  - suppress low-signal warnings
  - escalate only when action is clear

- Expose recent briefings and reasons in `pkg/admin`.

Success criteria:

- notifications feel like an operator, not a spammy bot
- daily summaries are grounded in real state
- incidents come with context and likely next actions

### 6. Production Observability And Admin Visibility

Goal: make debugging and trust much easier.

Implementation targets:

- Extend `pkg/admin` with panels for:
  - active provider route
  - current degraded mode
  - pending tasks and blocked tasks
  - recent autonomous actions
  - top failure reasons
  - budget and quota status

- Extend telemetry and sentinel signals.
  - CPU temperature
  - memory pressure
  - provider failure streaks
  - task backlog growth
  - deploy success rate

- Add smoke checks in deploy path.
  - After deploy, verify `/health`, expected port, and one representative route.
  - Relevant file: `scripts/deploy_coolify_restart.sh`

Success criteria:

- "is prod healthy?" answered from one screen
- deploy failures are caught immediately
- debugging runtime issues stops being guesswork

## Implementation Order

Do this in order. The later workstreams depend on the earlier ones.

### Phase 1. Reliability First

Scope:

- provider policy layer
- telemetry expansion
- context overflow mitigation
- richer `/health`

Primary files:

- `pkg/providers/*`
- `pkg/telemetry/*`
- `pkg/agent/loop.go`
- `cmd/picoclaw/gateway.go`

Why first:

Without this, every higher-level autonomy feature sits on a shaky runtime.

### Phase 2. Structured State

Scope:

- task, blocker, decision, reminder, commitment records
- retrieval rules for active state

Primary files:

- `pkg/state/*`
- `pkg/tools/tasks.go`
- `pkg/tools/agenda.go`
- `pkg/tools/reminder.go`
- `pkg/agent/context.go`

Why second:

The planner cannot be strong if the system does not know what is currently in flight.

### Phase 3. Planner Hardening

Scope:

- lifecycle state machine
- verification rules
- retry and follow-up behavior

Primary files:

- `pkg/agent/autonomy_plan.go`
- `pkg/agent/autonomy_agenda.go`
- `pkg/tools/workflow.go`
- `pkg/tools/toolloop.go`

Why third:

This is where PicoClaw starts to feel like an operator instead of a chatbot with tools.

### Phase 4. Safe Autonomy

Scope:

- policy enforcement
- audit log
- approval presets

Primary files:

- `pkg/tools/registry.go`
- `pkg/tools/self.go`
- `pkg/tools/deploy.go`
- `pkg/tools/git_tool.go`
- `pkg/tools/host_exec.go`
- `pkg/config/config.go`
- `pkg/admin/*`

Why fourth:

Only after state and planner are solid should autonomy permissions widen.

### Phase 5. Proactive Experience

Scope:

- briefings
- attention quality
- admin visibility

Primary files:

- `pkg/heartbeat/service.go`
- `pkg/attention/service.go`
- `pkg/sentinel/service.go`
- `pkg/admin/*`

Why fifth:

This adds the "Jarvis feel" after the foundations are dependable.

## Six-Week Delivery Plan

### Week 1

- provider policy table
- fallback and retry cleanup
- `/health` expansion
- provider telemetry counters

### Week 2

- structured task and blocker records
- state persistence cleanup
- context assembler for active work

### Week 3

- planner lifecycle states
- verification hooks
- retry and follow-up scheduling

### Week 4

- action classification
- autonomy approval policy
- audit logging

### Week 5

- briefing engine
- anti-noise alert rules
- blocked/stale work summaries

### Week 6

- admin dashboards
- deploy smoke tests
- final hardening and runtime tuning

## Acceptance Criteria For 8/10

PicoClaw reaches `8/10` when the following are true for at least a week in production:

1. It can survive provider issues without becoming unusable.
2. It can track active tasks, blockers, and next steps across sessions.
3. It can complete or properly defer multi-step work with verification.
4. It can act autonomously in low-risk domains with a visible audit trail.
5. Its proactive summaries are useful enough that you want them.
6. You can inspect health, failures, and autonomous actions from admin without digging through raw logs.

## Non-Goals

These are explicitly not required for `8/10`:

- full sci-fi multimodal ambient intelligence
- unrestricted autonomous code and infrastructure changes
- perfect general reasoning
- human-level social intuition

## First Concrete Tickets

If starting implementation now, the first ticket batch should be:

1. Add provider route state and degraded mode to `/health`.
2. Add telemetry counters for provider success, fallback, latency, and failure class.
3. Define structured state objects for tasks, blockers, and reminders in `pkg/state`.
4. Make `pkg/agent/context.go` pull active structured state into prompts.
5. Add planner execution states and verification outcome handling.
6. Add an autonomy audit log surfaced in admin.
7. Add deploy smoke checks to `scripts/deploy_coolify_restart.sh`.

## Bottom Line

The shortest path to `8/10` is:

- harden provider reliability
- formalize memory into structured operational state
- make the planner close loops
- enforce safe autonomy
- improve observability and proactive briefings

That is enough to make PicoClaw feel meaningfully closer to Jarvis without pretending it needs science fiction capabilities first.

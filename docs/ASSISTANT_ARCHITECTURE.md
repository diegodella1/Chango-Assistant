# Chango Architecture

This document describes Chango as it exists today, not as branding.

## What It Is

Chango is an autonomous assistant runtime built around:

- Persistent state and memory
- A configurable main LLM provider
- A local model for privacy-sensitive or cheap work
- Tool execution with real side effects
- Background loops that run without user prompts

It is stronger than a chatbot because it has memory, tools, identity, and recurring processes.
It is weaker than AGI because most cognition is still prompt orchestration around model calls.

## Current Layers

### 1. Identity and Prompt Surface

- Workspace markdown files define personality, operating rules, user profile, and heartbeat behavior.
- `ContextBuilder` assembles those files plus relevant memory and tool instructions into the system prompt.

Strength:
- Fast to iterate and highly expressive.

Weakness:
- Behavior can become prompt-fragile as capabilities grow.

### 2. Runtime Brain

- `AgentLoop` is the execution core.
- It receives inbound messages, builds context, calls the model, executes tools, and publishes replies.
- It also handles runtime provider/model switching and now supports live reload from config.

Strength:
- Single obvious control loop.

Weakness:
- Too much responsibility lives in one orchestration surface.

### 3. Provider Layer

- Main chat provider is selected from config.
- Optional local provider (`llamacpp`) supports monologue and privacy routing.
- Optional background provider supports cron/heartbeat/summarization escalation.
- Privacy Router can keep sensitive turns local and degrade gracefully.

Strength:
- Practical privacy/cost/performance tradeoff control.

Weakness:
- Model routing logic can become subtle and hard to explain without explicit health/status reporting.

### 4. Tools Layer

- Tools expose web, shell, Gmail, Calendar, Drive, wallet, file editing, lights, I2C/SPI, and more.
- This is where Chango becomes operational instead of conversational.

Strength:
- Real-world leverage.

Weakness:
- High side-effect risk if permissions and policies are too loose.

### 5. Memory and State

- Session history, summaries, vault notes, tasks, reminders, follow-ups, scoring, and telemetry live on disk.
- Memory is distilled rather than stored as a blind append-only transcript.

Strength:
- Better continuity than stateless assistants.

Weakness:
- Reconstruction is still per-turn; there is no true continuous internal stream.

### 6. Background Services

- Heartbeat
- Sentinel
- Attention
- Email watcher
- RSS watcher
- Health checks
- Cron
- Reasoning loop

Strength:
- Real autonomy between conversations.

Weakness:
- Service interactions are still loosely coupled and rely on shared files/config rather than stronger contracts.

## Real Limits

These are the current practical constraints:

- No continuous consciousness
- No guaranteed truthfulness from models
- No strong task planner with durable subgoals and rollback semantics
- No formal permission engine per capability
- Limited local reasoning depth
- Tool calling still depends on provider/model behavior
- Observability exists, but incident diagnosis is still harder than it should be

## What "Great Assistant" Should Mean Here

Not more prompt poetry. Not more integrations by themselves.

A great assistant here should mean:

- Clear capability boundaries
- Explicit permission policy for dangerous actions
- Predictable provider selection and failure modes
- Durable task execution with verification
- Better observability of why it chose what it chose
- Fewer silent fallbacks
- Safer money/code/email operations

## Recommended Roadmap

### Phase 1: Reliability

- Central provider validation and health checks
- Runtime status that explains selected provider, local provider, fallback, and privacy mode
- Clear startup failure for invalid config
- Better tests around provider routing, privacy routing, and reload behavior

### Phase 2: Permissioning

- Capability-based approval policy
- Separate policies for:
  - money
  - code changes
  - external communication
  - shell execution
  - destructive file operations
- Per-tool risk classification exposed in runtime/admin

### Phase 3: Task Execution

- Move from "single reply with tools" toward durable task runs
- Add:
  - task intents
  - checkpoints
  - verification steps
  - retry policy
  - explicit success criteria

### Phase 4: Cognitive Structure

- Split orchestration into narrower components:
  - planner
  - executor
  - verifier
  - memory writer
  - policy gate
- Reduce prompt sprawl by moving more rules into typed runtime decisions

### Phase 5: Trust and Safety

- Spend envelopes and stronger wallet guardrails
- Audit trails for side effects
- Human-readable incident log for "why Chango did X"
- Safer defaults for external comms and high-risk actions

## Bottom Line

Chango is already a serious autonomous assistant shell.
Its next leap is not "more intelligence" in the abstract.
It is stronger runtime structure: policy, observability, task durability, and explicit control over side effects.

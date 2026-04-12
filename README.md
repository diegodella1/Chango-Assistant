<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/monkey_1f412.png" width="120" alt="Chango">
</p>

<h1 align="center">Chango</h1>

<p align="center">
  <strong>Autonomous AI agent running on a Raspberry Pi 5</strong>
</p>

<p align="center">
  <em>Not a chatbot — a co-founder that thinks, acts, remembers, and improves on its own</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/Platform-Raspberry%20Pi%205-C51A4A?style=flat-square&logo=raspberrypi&logoColor=white" alt="Raspberry Pi">
  <img src="https://img.shields.io/badge/Channel-Telegram-26A5E4?style=flat-square&logo=telegram&logoColor=white" alt="Telegram">
  <img src="https://img.shields.io/badge/LLM-Multi--provider-8B5CF6?style=flat-square" alt="Multi-provider">
  <img src="https://img.shields.io/badge/Deploy-Coolify-6C47FF?style=flat-square" alt="Coolify">
</p>

---

## What is Chango?

Chango is an autonomous AI agent that lives on a Raspberry Pi 5. It thinks independently, acts proactively, remembers everything, and improves itself — all while running on a $80 computer in Buenos Aires.

It communicates via Telegram (and 13 other channels), uses 30+ tools, runs 7 background services, and has a personality with permission to disagree with you.

Built in Go. Containerized with Docker. Deployed via Coolify. Zero cloud dependencies beyond the LLM API.

---

## What Chango does on its own

These services run continuously without any user interaction:

| Service | Frequency | What it does |
|---------|-----------|-------------|
| **Heartbeat** | ~45 min | Buenos días, check-ins, follow-ups, task nudges |
| **Sentinel** | 2 min | Monitors CPU temp, RAM, disk — alerts on critical thresholds |
| **Attention** | 2 hours | Detects stale projects, overdue tasks, opportunities |
| **Email Watcher** | 30 min | Classifies Gmail inbox, notifies about important messages |
| **RSS Watcher** | 4 hours | Monitors feeds, filters by relevance, builds daily digest |
| **Health Check** | 5 min | Pings HTTP endpoints, auto-restarts downed containers |
| **Cron jobs** | Scheduled | Nightly reflection (23:00), weekly self-evaluation (Sundays 20:00), HN posting |

Chango also **auto-distills memories** — after each conversation, it extracts learnings and stores them in its Obsidian vault for future context.

---

## What Chango does when you talk to it

### Thinking (before responding)
- **Inner Monologue** — System 2 thinking via local model (zero cost, private)
- **Tree of Thought** — 3 parallel branches (pragmatic, contrarian, exploratory) → synthesis
- **Self-Critique** — Catches generic filler, yes-man behavior, intent mismatches
- **Council** — 3 specialized AI advisors deliberate before answering complex questions
- **Knowledge Graph** — Analogical reasoning across stored entities and relationships

### 30+ Tools

**Memory & Knowledge**
- Obsidian vault with TF-IDF semantic search (auto-injection on every message)
- Long-term preferences, daily notes, auto-distillation from conversations
- Knowledge graph for entity relationships and analogical reasoning

**Web & Research**
- Web search (Serper/Brave/DuckDuckGo), browse pages, fetch URLs
- YouTube transcript extraction

**Google Workspace**
- Gmail (read, search, send, reply)
- Calendar (events, appointments, multiple calendars)
- Drive (list, search, read documents)
- Real account ownership: Chango has its own mailbox, calendar access, and Drive access

**Communication & Media**
- Voice transcription (Groq STT) + Text-to-Speech (Edge TTS, es-AR-TomasNeural)
- Image generation (Pollinations.ai)
- Translation

**Automation**
- Shell execution (container + host via nsenter)
- Subagent system for parallel multi-step tasks
- Reminders, tasks, cron jobs, snippets
- HTTP requests to arbitrary APIs
- GitHub-aware workflows for code changes and deployments

**Smart Home**
- Magic Home WiFi lights (discovery, on/off, RGB, brightness)
- I2C / SPI hardware bus interaction

**Money**
- Lightning wallet via LNbits
- Can check balance, create invoices, and pay Bolt11 invoices within configured limits

**Security**
- Encrypted credential vault (AES-256-GCM)
- Privacy Router — sensitive data (DNI, cards, medical, financial) stays on local model
- Session locking: once classified sensitive, stays local

**Self-modification**
- Can read and edit its own operating instructions (AGENTS.md)
- Behavioral experiments (up to 3 active, evaluated weekly)
- Nightly distillation + weekly self-evaluation with auto-modification

---

## Architecture

```
                    ┌──────────────────────────────────────────────┐
                    │              Raspberry Pi 5                   │
                    │                                               │
  Telegram ────────►│  ┌───────────┐    ┌───────────────────────┐  │
  (+ 13 channels)   │  │  Channel   │───►│      MessageBus       │  │
                    │  │  Manager   │◄───│                       │  │
                    │  └───────────┘    └──────────┬────────────┘  │
                    │                              │                │
                    │  ┌───────────────────────────▼─────────────┐  │
                    │  │              AgentLoop                   │  │
                    │  │  ┌─────────────┐  ┌──────────────────┐  │  │
                    │  │  │   Context    │  │  Depth of        │  │  │
                    │  │  │   Builder    │  │  Reasoning        │  │  │
                    │  │  │  + Memory    │  │  • Monologue     │  │  │
                    │  │  │  + Skills    │  │  • Tree of Thought│  │  │
                    │  │  │  + Knowledge │  │  • Self-Critique  │  │  │
                    │  │  └─────────────┘  └──────────────────┘  │  │
                    │  └───────────────────────────┬─────────────┘  │
                    │                              │                │
                    │              ┌────────────────▼──────────────┐ │
                    │              │        ToolRegistry           │ │
                    │              │     30+ tools available       │ │
                    │              └───────────────────────────────┘ │
                    │                                               │
                    │  ┌─────────────────────────────────────────┐  │
                    │  │         Background Services              │  │
                    │  │  Heartbeat · Sentinel · Attention        │  │
                    │  │  Email · RSS · HealthCheck · Cron        │  │
                    │  └─────────────────────────────────────────┘  │
                    │                                               │
                    │  ┌─────────────────────────────────────────┐  │
                    │  │         Privacy Router                   │  │
                    │  │  Tier 1: Regex (instant)                 │  │
                    │  │  Tier 2: Local LLM (when needed)         │  │
                    │  │  Sensitive → local · Safe → cloud        │  │
                    │  └─────────────────────────────────────────┘  │
                    │                                               │
                    │  LLM: OpenAI · Anthropic · OpenRouter ·      │
                    │       Groq · Gemini · DeepSeek · LlamaCpp    │
                    └──────────────────────────────────────────────┘
```

### Key Components

| Component | Path | Purpose |
|-----------|------|---------|
| Agent Loop | `pkg/agent/loop.go` | Core message processing, LLM iteration, tool execution |
| Context Builder | `pkg/agent/context.go` | System prompt assembly (identity + skills + memory) |
| Depth of Reasoning | `pkg/agent/` | Monologue, Tree of Thought, self-critique, scoring |
| Channel Manager | `pkg/channels/` | 14 channels (Telegram, Discord, Slack, WhatsApp, etc.) |
| Tool Registry | `pkg/tools/` | 30+ tools — web, calendar, exec, memory, media, lights |
| Privacy Router | `pkg/providers/privacy_router.go` | Route sensitive data to local model |
| Memory Store | `pkg/agent/memory.go` | Obsidian vault with TF-IDF search |
| Services | `pkg/services/` | Email watcher, RSS watcher, health check |
| Sentinel | `pkg/sentinel/` | System health monitor with alerts |
| Attention | `pkg/attention/` | Proactive concern detection |
| Config | `pkg/config/config.go` | JSON config with env var overrides |

---

## Provider Routing

Chango is not tied to a single cloud model. It has:

- A **main chat provider** selected in `agents.defaults.provider`
- A **main model** selected in `agents.defaults.model`
- An optional **local provider** (`llamacpp`) for monologue, privacy routing, and cheap local work
- An optional **background provider** for cron / heartbeat / summarization escalation

Important behavior:

- If you select `openai`, Chango now uses `openai` or fails clearly. It does not silently fall back to `openrouter`.
- Provider changes made through the admin panel or `/provider` are reloaded into the live runtime.
- Sensitive turns may still be routed to the local model first by the Privacy Router, depending on policy.
- Tool-heavy turns can still prefer cloud when the local model is too weak for reliable tool calling.

---

## What It Can Actually Own

Chango is not "connected to" these things in an abstract sense. In this setup, they are part of its operating surface:

- **Email**: its own Gmail / Google Workspace mailbox
- **Calendar**: its own Google Calendar access
- **Drive**: read/search/upload/share operations on Google Drive
- **Wallet**: Lightning wallet through LNbits
- **GitHub**: repo access for reading, editing, committing, and deploying code
- **Telegram identity**: its own bot endpoint for ongoing conversations

That means it can do real actions, not just talk about them.

---

## Limits

Chango is powerful, but it is still an orchestration system around models, tools, and policies. Its practical limits matter:

- It does **not** have continuous consciousness. Most "thought" is reconstructed from files, memory, and the current turn.
- It does **not** learn arbitrary new skills from one example. It improves through notes, prompt updates, experiments, and code changes.
- It does **not** guarantee correctness. Web research, tool use, and cloud model outputs can still be wrong.
- It is only as capable as its configured credentials and tools. No API key, no action.
- Local models are cheaper and more private, but weaker for long-context reasoning and tool calling.
- Cloud providers are stronger, but introduce token cost, rate limits, outages, and privacy tradeoffs.
- Wallet access should be treated as high-risk capability and bounded with strict spend limits.
- Email, Drive, shell, and GitHub access mean misconfiguration can cause real side effects. This is an autonomous system, not a toy.

---

## Quick Start

### 1. Clone and configure

```bash
git clone https://github.com/diegodella1/Chango-Assistant.git
cd Chango-Assistant
cp config/config.example.json ~/.picoclaw/config.json
```

### 2. Fill in your API keys

Edit `~/.picoclaw/config.json`:

```jsonc
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "YOUR_TELEGRAM_BOT_TOKEN",
      "allow_from": ["YOUR_TELEGRAM_USER_ID"]
    }
  },
  "providers": {
    "openai": {
      "api_key": "sk-..."
    }
  },
  "tools": {
    "web": {
      "serper": {
        "enabled": true,
        "api_key": "YOUR_SERPER_API_KEY"
      }
    }
  }
}
```

Pick the active cloud provider explicitly:

```jsonc
{
  "agents": {
    "defaults": {
      "provider": "openai",
      "model": "gpt-5"
    }
  }
}
```

If `provider` is set to `openai` but no OpenAI credential is configured, startup should fail clearly instead of silently routing to another backend.

### 3. Build and run

```bash
# Local
go build -o picoclaw ./cmd/picoclaw && ./picoclaw gateway

# Docker
docker build -t chango . && docker run -v ~/.picoclaw:/root/.picoclaw chango
```

---

## Personality

Chango's behavior is defined by markdown files in the workspace:

| File | Purpose |
|------|---------|
| `AGENTS.md` | Operating instructions — execution protocol, tool APIs, behavioral rules |
| `SOUL.md` | Personality — mentors (Thiel, Jobs, Musk), humor, communication style |
| `IDENTITY.md` | Core identity and name |
| `USER.md` | User profile, preferences, account information |
| `HEARTBEAT.md` | Proactive tasks and check-in behavior |

These files are loaded into the system prompt at runtime. Chango can modify its own `AGENTS.md` during weekly self-evaluation.

---

## Deployment

Runs as a Docker container deployed via [Coolify](https://coolify.io/) on a Raspberry Pi 5:

1. Push to GitHub
2. Trigger Coolify restart via API
3. Multi-stage Dockerfile: Go build → Debian bookworm runtime (python3 + edge-tts + ffmpeg)
4. Volume mount: `~/.picoclaw` for config, workspace, and persistent data

---

## In one line

**Alone**: monitors, reflects, remembers, alerts, improves itself.
**Assisted**: thinks deep, executes with 30+ tools, challenges assumptions, learns from every interaction.

---

<p align="center">
  <em>Built on a Raspberry Pi 5 in Buenos Aires — Go + Docker + Coolify</em>
</p>

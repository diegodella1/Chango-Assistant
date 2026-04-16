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

**Communication & Media**
- Voice transcription (Groq STT) + Text-to-Speech (Edge TTS, es-AR-TomasNeural)
- Image generation (Pollinations.ai)
- Translation

**Automation**
- Shell execution (container + host via nsenter)
- Subagent system for parallel multi-step tasks
- Reminders, tasks, cron jobs, snippets
- HTTP requests to arbitrary APIs

**Smart Home**
- Magic Home WiFi lights (discovery, on/off, RGB, brightness)
- I2C / SPI hardware bus interaction

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

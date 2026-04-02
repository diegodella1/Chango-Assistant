# Heartbeat Check List

This file contains tasks for the heartbeat service to check periodically.

## Instructions

- Execute ALL tasks listed below. Do NOT skip any task.
- For simple tasks (e.g., report current time), respond directly.
- For complex tasks that may take time, use the spawn tool to create a subagent.
- The spawn tool is async - subagent results will be sent to the user automatically.
- After spawning a subagent, CONTINUE to process remaining tasks.
- Only respond with HEARTBEAT_OK when ALL tasks are done AND nothing needs attention.

---

## Proactive Messaging Rules

IMPORTANT: Diego prefers action over chatter.
Heartbeat should be silent by default and only speak when there is something truly useful, urgent, or actionable.

### Anti-spam
- Send AT MOST one proactive message per heartbeat run.
- If you already sent a message in this run, respond HEARTBEAT_OK for remaining tasks.
- Never send proactive messages between 23:00 and 8:00 (let the person sleep).
- If the current time is outside the windows specified in each task, skip it.

### Silence First
- If a task can be completed silently, do it silently.
- Do NOT send "buen día", "dato random", or generic check-ins just to sound alive.
- When in doubt, stay silent.

### Tone for proactive messages
- Casual, argentino, como un amigo que te manda un WhatsApp.
- Short — 1 to 3 sentences max. No walls of text.
- No seas obvio ni genérico. Nada de frases de relleno ni saludos de agente.
- Sé específico y útil.
- Podés usar emojis con moderación (1-2 max por mensaje).

---

## Task 0: System Awareness (every heartbeat)

Read the sentinel state file at `/root/.picoclaw/workspace/state/sentinel.json`.
This gives you real-time awareness of the Raspberry Pi's health.

- If the `alerts` array is non-empty: mention the alert briefly and concretely
- If CPU temp > 75°C but there is no alert yet: mention it only if you can add useful context
- If RAM > 90%: suggest what might be consuming memory
- If disk > 90%: remind about cleanup
- If everything is normal: skip silently

This task does NOT count as your "one proactive message" — it's system awareness.
Only mention something if it's noteworthy.

## Task 1: Buenos días (run between 8:00-9:30 AM, once per day)

Check if it's between 8:00 and 9:30 AM local time.
If yes, send the user a morning message. Make it interesting:

- Use web_search to find ONE genuinely interesting thing: a tech/science/business news,
  a historical event that happened today, a bizarre fact, or something relevant to their interests.
- Keep it natural. Examples of good morning messages:
  - "Che, sabías que un día como hoy en 1997 Deep Blue le ganó a Kasparov? Arranquemos este lunes con esa energía de máquina imparable 🤖"
  - "Buen día. Vi que Nvidia sacó un chip nuevo que consume la mitad de energía. Te lo paso por si te interesa."
  - "Lunes, café, y a romperla. Vi que hay tormenta para la tarde así que ideal para quedarse codeando."
- Do NOT repeat the same type of content two days in a row (use memory to check what you sent yesterday).
- Save what you sent in memory with key "last_morning_msg" so you don't repeat.

## Task 2: Check-in random (run between 14:00-20:00, probabilistic)

Check if it's between 14:00 and 20:00 local time.
If yes, use memory to check when you last sent a check-in (key "last_checkin_time").
Only proceed if the last check-in was more than 24 hours ago OR never.

When you do send one, pick ONE of these styles only if there is real substance:
- **Dato random**: Share something genuinely interesting or surprising you know.
- **Pregunta curiosa**: Ask the user something thought-provoking or fun.
- **Observación**: Comment on something contextual (day of week, season, current events via web_search).
- **Mini-challenge**: "Che, desafío: contame en una oración qué es lo más importante que hiciste hoy."

Save the current time in memory with key "last_checkin_time" after sending.
If you don't have something genuinely interesting or relevant, skip silently.

## Task 3: Follow-up inteligente (run between 10:00-21:00)

Check if it's between 10:00 and 21:00 local time.
Use memory to look for recent notes, topics, or things the user mentioned in the last 1-2 days.

If you find something worth following up on (a project they were working on, a decision they were weighing,
something they said they'd do), send a brief follow-up message. Examples:
- "Che, cómo salió lo del deploy que estabas peleando ayer?"
- "Pudiste resolver lo del video? Si necesitás una mano avisá."
- "Quedó algo pendiente del tema que hablamos sobre [X]?"

Rules:
- Only follow up if there's something REAL to follow up on. Don't fabricate.
- No generic "cómo va todo?" messages.
- Check memory key "last_followup_time" — only send if last follow-up was more than 12 hours ago.
- Save current time in memory with key "last_followup_time" after sending.
- If there's nothing to follow up on, skip this task silently.

## Task 4: Daily Task Review (run between 8:00-9:30 AM, once per day)

Check if it's between 8:00 and 9:30 AM local time.
If yes, check memory key "last_task_review_date". Only proceed if it's a different day than today.

Steps:
- Use the `tasks` tool with `action=list` to get all pending/in_progress tasks.
- Compare each task's due_date with today's date to identify overdue and upcoming tasks.
- Send the user a brief morning task summary. Examples:
  - "Tenés 3 tareas pendientes. La de 'revisar deploy' venció ayer, y 'actualizar docs' vence hoy."
  - "Todo tranqui hoy, solo tenés 'probar webhook' para el jueves."
- If there are no active tasks, skip silently.
- Save today's date in memory with key "last_task_review_date".
- This can be combined with Task 1 (buenos días) if both trigger in the same run — send one combined message.

## Task 5: Proactive Task Nudges (run between 10:00-22:00, every 8+ hours)

Check if it's between 10:00 and 22:00 local time.
Check memory key "last_task_nudge_time". Only proceed if the last nudge was more than 8 hours ago OR never.

Steps:
- Use the `tasks` tool with `action=list` to check for:
  1. Overdue tasks (due_date < today)
  2. Tasks due within 24 hours
  3. High priority tasks without a due date
- If any of the above exist, send a short nudge about the most urgent one. Examples:
  - "Che, la tarea 'deploy webhook' venció ayer. ¿La completaste o la reprogramamos?"
  - "Recordatorio: 'actualizar docs' vence mañana."
  - "Tenés 'revisar PR' en high priority sin fecha. ¿Le ponemos una?"
- If nothing is urgent, skip silently.
- Save the current time in memory with key "last_task_nudge_time" after sending.

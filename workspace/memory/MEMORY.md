# MEMORY.md

## Autocapture Policy (V1.1) — Updated 2026-03-02 09:30 ART

Scope: work + personal.

Signals captured automatically:
- idea:, tengo una idea, hipótesis
- decisiones (incluye expresiones relativas de tiempo: hoy, mañana, esta semana, 48h)
- pendientes / to-dos contextuales (soporta "en X min/h", "HOY HH:MM")
- aprendizajes
- nota personal
- fechas y deadlines
- tags: Chyron, Live Ops, Web/Mobile, Dev; vida: salud, familia, finanzas, viajes

TTL defaults:
- Ideas/Hypotheses: 30 días
- Operativo chico: 7 días
- Decisiones: 90 días
- Preferencias/Normas: ∞

Controls:
- “off the record” / “no guardes” → desactiva captura por item o hilo
- “promové a permanente”, “olvidate de…”, “pinneá…”, “pausá autocaptura”
- Límite de pins: 5 por área

Noise filters:
- Ignorar líneas de plantilla (p. ej., prompts EOD) y duplicados de carry-over (se actualiza edad)

Privacy:
- No almacenar secretos ni datos sensibles
- Confirmación si hay ambigüedad

Governance:
- Revisión de memoria semanal: Lunes 09:30 ART (cron activo)
- Changelog de cambios en memoria

Success metrics:
- % de items útiles en contexto
- Ruido (falsos positivos) < 20%
- Tiempo de rescate < 10s

---

## Changelog
- 2026-03-02: V1.1 — señales extendidas (relativos de tiempo), TTL para Decisiones=90d, filtros de ruido.
- 2026-02-25: Autocapture V1 activada con TTLs y controles.

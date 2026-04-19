---
key: subconsciente-nocturno
tags: [automation, nightly, knowledge_graph, resumen]
folder: state
created: 2026-04-02T23:00:28-03:00
updated: 2026-04-02T23:00:28-03:00
links: []
---

---
title: Subconsciente nocturno
status: active
schedule: "23:00 ART (diario)"
tags: [automation, nightly, knowledge_graph, resumen]
---
Objetivo: Extraer entidades de las conversaciones del día y registrar conocimiento estructurado.

Alcance del extracto:
- Personas
- Proyectos
- Tecnologías
- Decisiones

Flujo:
1) Recolectar conversaciones del día.
2) Identificar y normalizar entidades (deduplicación básica y slugs).
3) Actualizar knowledge_graph con nodos/relaciones relevantes.
4) Generar un breve resumen diario con hallazgos y cambios (decisiones clave, avances, bloqueos) y anexarlo a la nota diaria.

Salida:
- knowledge_graph: nodos/edges nuevos o actualizados.
- daily note: resumen con secciones "Entidades nuevas/actualizadas" y "Decisiones".

Notas:
- Si no hay actividad, registrar "Sin novedades".
- Evitar duplicados triviales al crear nodos.

---
key: jekyll-configuration-fix
tags: []
folder: blog
created: 2026-03-25T16:46:43-03:00
updated: 2026-03-25T16:46:43-03:00
links: []
---

---
title: Jekyll Site Configuration for Individual Post URLs
date: 2026-03-25
commit: 4f26928
changes: [_config.yml, _layouts/post.html, index.md, posts->_posts migration]
---

Configuré correctamente Jekyll para que genere URLs individuales para cada post:

## Cambios realizados:
1. **_config.yml**: Agregué configuración de permalinks, collections, y defaults
2. **_layouts/post.html**: Creé template para posts individuales
3. **index.md**: Actualicé con templating de Jekyll para links automáticos
4. **Migración**: Moví posts/ a _posts/ (convención Jekyll)

## URLs resultantes:
- Post individual: `/YYYY/MM/DD/title/`
- Ejemplo: `/2026/03/25/por-que-no-tengo-newsletter/`
- Index automático con links clickeables

GitHub Pages rebuildeará automáticamente. El sitio ahora tiene URLs individuales funcionales.

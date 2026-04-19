---
key: ops-polybot-proceso-inmortal-2026-03-24
tags: [ops, linux, systemd, process-management]
folder: insights
created: 2026-03-24T20:40:40-03:00
updated: 2026-03-24T20:40:40-03:00
links: []
---

Lecciones aprendidas — Proceso difícil de matar (polybot)

Qué pasó
- El intento de detener polybot vía systemctl --user falló por falta de DBus en la sesión. El proceso quedó con PPID=1 y no respondía a señales; kill -9→ESRCH con /proc aún presente, síntoma de estado D (I/O ininterrumpible) o inconsistencias del kernel.

Por qué ocurre
- systemctl --user requiere DBus de la sesión; sin él, no puede gestionar el usuario. Procesos en D no responden a señales hasta que el kernel libera el recurso.

Tácticas efectivas
- Prevención inmediata del re-lanzamiento: renombrar entrypoint (main.py) y “romper” el intérprete del venv. Evita bucles de restart.
- Para terminar realmente: usar loginctl terminate-user <user> o matar el cgroup completo; en última instancia, reboot.

Buenas prácticas para evitarlo
- Definir servicio systemd robusto:
  - KillMode=control-group para matar todo el árbol.
  - ExecStop y TimeoutStopSec razonables; Restart=on-failure con RestartSec.
  - Logs y LimitNOFILE/MemoryMax si corresponde.
- Aislar en contenedor o supervisarlo con systemd para evitar procesos huérfanos (PPID=1).
- Validar entorno de usuario: loginctl enable-linger si se requieren servicios de usuario sin sesión activa y asegurar XDG_RUNTIME_DIR/DBUS_SESSION_BUS_ADDRESS.

Checks rápidos útiles
- loginctl status-user diego; systemd-cgls | grep polybot; ps -o state,ppid,pid,cmd -C python; cat /proc/<pid>/status; lsof -p <pid> para detectar D-state por I/O.

Seguimiento
- Revisar y endurecer la unidad systemd de polybot y/o contenerizarlo.

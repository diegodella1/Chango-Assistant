---
key: host-exec-systemctl-limitation
tags: []
folder: insights
created: 2026-04-15T22:00:06-03:00
updated: 2026-04-15T22:00:06-03:00
links: []
---

When using `host_exec`, direct interaction with `systemctl` commands that rely on D-Bus (e.g., `systemctl enable`, `systemctl start`) is not possible. This is because `host_exec` runs commands in an environment that lacks D-Bus connectivity, which `systemctl` requires for certain operations. This necessitates manual intervention for tasks like enabling and starting systemd timers or services.

---
key: insight-ffmpeg-7-0-20260322
tags: [insight_for_diego, av, ffmpeg, self-hosting, pipelines]
folder: insights
created: 2026-03-22T14:03:27-03:00
updated: 2026-03-22T14:03:27-03:00
links: []
---

FFmpeg 7.0 “Dijkstra” was released. Notables: experimental native VVC (H.266) decoder, IAMF support, and multi-threaded ffmpeg CLI. Implications: better transcoding throughput on Pi 5, improved modern codec decode paths, and potential for cleaner audio pipelines. Consider bumping containers and testing flags (-threads, hwaccel where available) in our AV workflows.

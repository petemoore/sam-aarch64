# `docs/process/` — autonomous-development workflow

This directory defines the two-tier model for running autonomous development sessions on this project.

**The model in one sentence:** a persistent Conductor session spawns serial Builder sessions; each Builder implements one item to completion and ends; the Builder does not know the Conductor exists.

## Docs

| File | Role | Launch prompt |
|---|---|---|
| [`conductor.md`](conductor.md) | The Conductor playbook — a persistent controller that sequences work, spawns Builders, runs a 30-min watchdog, and parks only when the whole-project non-Pete queue is empty. | "Read `docs/process/conductor.md` and begin." |
| [`builder.md`](builder.md) | The Builder brief — a focused session that implements one item, lands a PR, leaves `main` clean, and ends. | (Spawned by the Conductor with a task-specific prompt that includes a pointer to this file.) |

Pete launches the Conductor; the Conductor spawns Builders.
The two files are kept separate so the roles stay distinct — a Builder session never needs to know about orchestration.

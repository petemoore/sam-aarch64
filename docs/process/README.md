# `docs/process/` — autonomous-development workflow

This directory defines the two-tier model for running autonomous development sessions on this project.

**The model in one sentence:** a thin Conductor loop (Pete's interface) keeps exactly one Builder alive at a time, relays its status and open questions to Pete, and folds Pete's feedback into the next Builder; each Builder decides what to work on, implements it to completion, and ends.

## Docs

| File | Role | Launch prompt |
|---|---|---|
| [`conductor.md`](conductor.md) | The Conductor playbook — a thin persistent loop (Pete's interface) that spawns one Builder at a time, relays its status and open `qN` questions to Pete, folds Pete's feedback into the next Builder, runs a 30-min watchdog, and stops when the Builder reports the queue is drained. | "Read `docs/process/conductor.md` and begin." |
| [`builder.md`](builder.md) | The Builder brief — a smart, autonomous session that evaluates the queue, picks the next item, does the work, lands a PR, updates the ROADMAP, and ends. | "Read `docs/process/builder.md` and begin (continue per `docs/ROADMAP.md`)." |

Pete launches the Conductor; the Conductor spawns Builders.
The two files are kept separate so the roles stay distinct — a Builder session never needs to know about orchestration.

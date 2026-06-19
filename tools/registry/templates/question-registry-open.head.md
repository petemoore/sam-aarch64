# Question registry — open

Open questions (`qN`) that gate one or more work items. There is no closed view:
a question is deleted once answered (see lifecycle below).

## Id conventions

`q<N>` or `q<N><letter>`. True-numeric sort. Ids are globally unique across the
item and question spaces; never reused.

## Transient-question lifecycle

1. A question is raised when an item cannot proceed without a decision.
   The gated item declares `depends_on: [qN]`.
2. Pete (or whoever is `owner`) answers the question.
3. An agent curates every dependent item — applying the decision: redefine,
   split, mark `WONTFIX`, spawn new items, raise follow-up questions.
4. Once every `depends_on: [qN]` edge has been removed from all items, the
   question is **deleted** from this file. Git history is the archive.

The validator enforces the delete-gate: a question with live `depends_on`
dependents cannot be deleted (the dangling edge would fail inv 11).

Standalone questions (not gating any specific item) resolve the same way:
the answer either spawns a new work item or the question is simply deleted
with the rationale in the git commit message.

<!-- The table below is generated — do not edit it by hand. -->


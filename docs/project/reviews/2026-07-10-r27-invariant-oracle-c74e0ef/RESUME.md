# RESUME POINTER — R27 branch adversarial review (c74e0ef)

**RESOLVED 2026-07-10 by hand-adjudication — resume no longer required.** After two lifecycle
deaths, the criticals were adjudicated from the banked journal + executable reproduction rather
than by paying the resume tax a third time. See [`README.md`](README.md) for the dispositions.
This file is kept as the record of how recovery would have worked, and because the journal snapshot
it points at is the primary evidence.

Status: **DONE (hand-adjudicated).** The Review + Attest phases are cached in the snapshot; the Judge
panel was partial (~18 judges) and was superseded by hand-adjudication. Do NOT treat the partial
harness result alone as green — the README is the authority.

## State (update this line each time you touch it)

- Run ID: `wf_5ed1383f-2ce`
- Journal: `~/.claude/projects/-workspaces-jobtree/b3df7d06-fa76-4793-98b7-31b44b11906e/subagents/workflows/wf_5ed1383f-2ce/journal.jsonl`
- Last known: **58 of ~112 agents completed** (Scout + 3 Review lenses + Attest + 9 judges). Remaining
  = the skeptic Judge panel for the criticals.
- Reviewed commit: `c74e0ef` on `fix/r27-invariant-oracle`.

## Resume (replays all completed agents from cache for FREE; only re-runs dead judges)

The exact args are the ones already embedded in the last workflow result. To resume, re-invoke the
workflow with `resumeFromRunId: 'wf_5ed1383f-2ce'` and the SAME args (recover them from the last
`<task-notification>`'s `<diagnostics>` resume line, or from
`scratchpad/resume_args_compact.json` if that session's scratch survived):

```
Workflow({ scriptPath: '/workspaces/jobtree/.claude/workflows/adversarial-review.js',
           resumeFromRunId: 'wf_5ed1383f-2ce',
           args: <the same review args> })
```

Resume is **same-session-and-same-disk only**: it reads the journal above. A codespace *stop/start*
preserves it; a *rebuild* destroys `~/.claude` and resume can no longer cache-replay — in that case,
hand-adjudicate from `journal-snapshot.jsonl` in this directory (a rebuild-proof copy).

## Why this pointer exists

Two lifecycle deaths in a row (session-limit reset 9am UTC, then a codespace restart) each forced a
resume that re-ran the in-flight judges. The completed agents were never lost — they journal to disk —
but finding the resume command meant digging through a 60 MB transcript. This file is the one-step
recovery so that never costs an archaeology session again.

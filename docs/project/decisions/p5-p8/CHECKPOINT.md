# Checkpoint — 2026-07-26, quota exhausted mid-exercise

Written so this can be picked up cold. Nothing here is a decision; decisions are David's and are
listed under "Waiting on David".

## The one thing that must not be lost

**Do NOT merge #127.** Its head (`37270af`) carries a **confirmed cross-tenant denial of service**,
reproduced by execution: a Budget created in *any* namespace, naming `owner: org:team`, flips
`OwnerOf("default")` to `""` and terminally fails `default`'s reservations in one tick. Nothing in
the victim namespace changes. `Budget.Spec.Owner` is a free-form string the victim does not control.

The autopilot's own words: *"the unsafe option is live on the branch right now"* — terminal behaviour
is committed, so P8's option (4) is in force **by accident rather than by decision**. It was not
reverted because reverting reinstates a different confirmed HIGH (the immortal reservation). Both
endpoints of P8 are proven defective. That is the whole reason for the design exercise below.

## In flight

| what | id | state / how to resume |
|---|---|---|
| P5-P8 design exchange | workflow `wf_4ab45948-420` | resume: `Workflow({scriptPath: "<session>/workflows/scripts/p5-p8-design-exchange-wf_4ab45948-420.js", resumeFromRunId: "wf_4ab45948-420"})`. Fable's opening position is cached; the relay calls re-run because their contract changed. |
| fix-diff adversarial panel | workflow `wf_5a636517-097`, Actions run `30186226379` | segment 2 of the autopilot chain, resuming Judge over 16 findings. Session artifact `session-6ffcd803-141f-446d-bbef-8fa9df9954ae`. |

**The design exchange has a bug fixed but unverified.** The first attempt recorded a *working*
cross-vendor seat as dead: liveness was tested with `/CODEX UNAVAILABLE/` against free prose, and the
relay opened with *"**CODEX UNAVAILABLE:** No — codex ran successfully."* The substring matched, Sol's
half of the exchange was skipped, and the run degraded to one vendor talking to itself. Liveness is
now a structured boolean (`codexRan`) the model sets deliberately, with codex's verbatim answer and
the relay's own commentary in separate fields. **This is the third instance today of one shape:
a sentinel matched in prose firing on prose *about* the sentinel.**

## Waiting on David

1. **P5, P6, P7, P8** — the four parked owner decisions. P5/P6/P7 are written up in full at
   `git show 37270af:docs/project/DECISIONS-NEEDED.md`. **P8 is not yet in the repo** — it exists
   only in issue #132 comments dated 2026-07-25T23:17Z and 23:21Z, and is reproduced verbatim in
   `BRIEF.md` beside this file. Writing P8 into `DECISIONS-NEEDED.md` is outstanding work.
2. **Whether to post an `@autopilot` bounding directive** on issue #132: *finish Judge, archive,
   stop; attempt no fix that depends on P8.* Without it the chain can re-dispatch up to 24 times and
   will resume "fix things" with no ruling to fix toward — restarting the oscillation autonomously.
3. **PR #142** — open; codex model flag + the tree-vs-commit rail.

## How the decision gets made and known to be correct

Agreed method, not yet executed:

1. **Enumerate, do not choose.** `(cause of principal loss) x (what the run holds) x (how long it has
   persisted)`. "Hold vs terminate" is the wrong shape — both were offered as *global* answers to a
   question whose answer varies by cell, which is what produced the oscillation.
2. **Find what is already decided.** Ratified text binds (`AGENTS.md:176`). `quota-semantics.md`,
   the R7 amendment (§4/§5, C-2, C-4) and `ConsumedGPUHours`' doc comment already reach some cells.
   Only the residue is open, and it is probably much smaller than four options implies.
3. **Turn the panel around.** Free hunting against unspecified behaviour cannot terminate — that is
   what has been happening. Given a *stated* rule, ask bounded questions instead: is a state left
   undefined? does obeying it violate a ratified invariant? can a principal outside the trust
   boundary induce it? Those terminate. Loop until two consecutive rounds find nothing new.
4. **Encode before implementing.** An `INV-*` in `pkg/invariant`. A rule that cannot be stated as an
   invariant is not yet decided — difficulty encoding it is information, not an obstacle.
5. **Record the residue as a ruling**, with rejected alternatives and evidence. For a policy question
   the decision does not discover a truth, it creates one; writing it down IS the correctness
   mechanism, and it is what makes a future lens's "but terminating destroys X" an answered question
   rather than another round of fixes.

**Constraints the reproductions already derived** (these narrow the answer, and were paid for):
**non-inducibility** — no principal outside a namespace may destroy its reservations (the squatter
Budget); and **durability** — the condition must persist (a `kubectl delete && apply` window must not
destroy anything).

## Landed today (all merged to main)

#133 session persistence across segments · #134 `@autopilot` marker for directives · #135 P6 (now
renumbered P7) · #136 cross-vendor trace seat + `judgeOnly` · #137 attest against the pinned commit ·
#138 review archive · #139 sentinel moved outside the repo · #140 exit-trap rescue of unpushed work ·
#141 Codex on the ChatGPT subscription.

Open: **#142** (codex model flag + tree-drift rail). Branch `wip/recovery-test-assertion` (`37270af`)
holds a repaired test — the original passed with the repair deleted — to fold into #127 once its
branch unfreezes.

## Operational facts worth not re-deriving

- **Codex on a ChatGPT subscription rejects every explicitly named model** (`-m gpt-5.6`,
  `gpt-5-codex`, `gpt-5` all 400). Omit `-m`; the account serves `gpt-5.6-sol`. Under an API key the
  opposite holds. #142 makes `-m` opt-in via `args.codexModel`.
- **Probe codex by forcing a filesystem read**, never by exit code — a seat whose shell tool is dead
  still exits 0 and degenerates into web-searching while looking alive.
- **A branch under review is frozen.** "It is only a comment" is not an exemption: a comment
  displaces line numbers exactly as much as code does. #142 adds the rail that refuses to run when
  the tree is not the reviewed commit.
- **Attribution so far (n=1 full panel):** codex earns its seat as a *finder* — 2 HIGH findings no
  Claude lens raised, one reproduced by compiled test — but not as a *judge*: `decisive=0`. That
  looks structural, not sample noise. A reproduction confirms alone in this harness, so verdicts turn
  on executing code, and the read-only sandbox compiles nothing.

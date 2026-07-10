export const meta = {
  name: 'adversarial-review',
  description: 'Fail-closed adversarial review: a lens that produces no real work BLOCKS, it never reads as green',
  whenToUse: 'Reviewing any change to the funding engine, the scheduler plugin, or another sole-committer path. Pass args: {context, commit, lenses:[{name,model,effort,prompt,questions}], minEvidence, skepticQuorum}. The four standard lenses always run; caller lenses are ADDITIONAL. There is no refuteThreshold: the skeptic panel is heterogeneous and its aggregation is asymmetric, not a vote count.',
  phases: [
    { title: 'Scout', detail: 'mechanically scan the diff for the taxonomy tells' },
    { title: 'Review', detail: 'standard + caller lenses investigate, cite evidence, and are validated' },
    { title: 'Attest', detail: 'independently verify every citation is real' },
    { title: 'Judge', detail: 'a full quorum of skeptics per finding' },
  ],
}

// ---------------------------------------------------------------------------
// WHY THIS EXISTS
//
// A review lens once returned summary:"test" with a finding titled "a" and the
// scenario "b" — pure schema-filling, zero work. It contributed no findings, and
// zero findings read as "clean". Three skeptics then earnestly refuted the
// placeholder. The panel looked unanimous because a member never showed up, and
// the real regression it was assigned to find shipped.
//
// The rule this file enforces: SILENCE IS NOT CONSENT. Absence of a finding is
// only evidence of absence when the agent demonstrably did the work. So:
//
//   1. Every lens must answer every assigned question and cite real evidence
//      (file, line, verbatim quote). Output is validated, not trusted.
//   2. Citations are checked by an independent agent against the actual files.
//      A fabricated or unquotable citation invalidates the lens.
//   3. A lens that cannot produce valid output after retries does not "pass" —
//      it BLOCKS the whole review. The verdict can never be GREEN without it.
//   4. Skeptics need a full QUORUM. A dead or degenerate skeptic is not a vote.
//      Under-quorum findings are surfaced as UNRESOLVED, never silently dropped
//      — otherwise two crashed agents bury a real bug.
//
// SECOND-GENERATION ADDITION (after six consecutive confirmed defects):
//
//   5. The CALLER used to choose the lenses. A caller who forgot to ask about
//      lease lifecycle got a green review — an unenforced obligation, which is
//      the exact bug class this harness exists to catch, reproduced in the
//      harness itself. STANDARD_LENSES now always run. They cannot be removed,
//      overridden, or suppressed by args. Caller lenses are additional.
//   6. Reviewers are pointed at docs/project/adversarial-review-playbook.md,
//      which carries the taxonomy of the six real defects: the tell, where to
//      look, and how to confirm. It is mandatory reading, not background.
//   7. A SCOUT phase greps the diff for each taxonomy tell and hands every lens
//      a list of PRIORITY LEADS. "Where to look" should be computed, not recalled.
//
// THIRD-GENERATION (after a panel of three Opus skeptics refuted a TRUE finding, and
// four more skeptics died holding verdicts they could not serialize):
//
//   8. INVESTIGATORS WRITE PROSE. A cheap model maps the prose onto the schema. Making
//      a thinking model emit JSON while it reasons killed four judges mid-verdict; three
//      of them had already decided. The shaper repairs the SHAPE and never the
//      SUBSTANCE — it may not add, infer or invent, and must report `unsupported`
//      rather than fabricate. Otherwise a lens that did no work gets laundered into a
//      well-formed report and the fail-closed rail says GREEN.
//   9. THE SKEPTIC PANEL IS HETEROGENEOUS AND THE VOTE IS NOT A VOTE. Three skeptics on
//      one model are three samples of one distribution; they fail together. Sonnet
//      reproduces, Opus traces, Fable weighs consequence. A reproduction CONFIRMS alone;
//      a refutation needs BOTH the trace and a reproduction that was tried and failed;
//      and the consequence lens may veto a FIX without touching the FINDING.
// ---------------------------------------------------------------------------

// The Workflow tool sometimes delivers `args` as a JSON-encoded string rather
// than a value. Accept both: rejecting the string form would make this harness
// fail to RUN, which is the one failure mode it exists to prevent.
let A = args || {}
if (typeof A === 'string') {
  try {
    A = JSON.parse(A)
  } catch (e) {
    throw new Error(`adversarial-review: args is a string but not valid JSON: ${e.message}`)
  }
}
if (!A.context) {
  throw new Error('adversarial-review requires args: {context, [commit], [lenses]}')
}
const MIN_EVIDENCE = A.minEvidence || 3
const SKEPTIC_QUORUM = A.skepticQuorum || 3
const MAX_ATTEMPTS = 3
const DIFF = A.commit ? `git show ${A.commit}` : 'git diff main...HEAD'

const RULES = `
HARD RULES (violating any is a task failure):
- NEVER run a mutating git command (no checkout/restore/revert/stash/add/commit/clean/reset).
  'git log', 'git show', 'git diff', 'git status' are allowed.
- NEVER spawn sub-agents.
- NEVER create, edit, or delete a file in the repository. Scratch files go only under a temp dir.

OUTPUT CONTRACT — enforced by an automated validator, not by good faith:
- 'summary' must be a full paragraph (>=200 chars) describing what you actually did and concluded.
- 'answers' must contain ONE entry per assigned question, each with a substantive conclusion
  (>=80 chars). If you did not investigate a question, say so explicitly in its conclusion — do not
  invent an answer, and do not omit the entry.
- 'evidence' must contain at least ${MIN_EVIDENCE} citations: {file, line, quote}, where 'quote' is
  text copied VERBATIM from that file at approximately that line. An independent agent will open the
  files and check. A fabricated citation invalidates your entire report.
- Placeholder values ("test", "a", "b", "TODO", "n/a", "none") are a task failure.
- Finding NOTHING is a perfectly good answer: return an empty 'findings' array and explain in the
  summary WHY the code is sound. But you must still answer every question and cite evidence.
`

// Every lens and every skeptic reads this. It is the single source of truth for the
// taxonomy; this file deliberately does NOT restate it, so the two cannot drift.
const PLAYBOOK = `
MANDATORY READING — do this BEFORE anything else:
  Read docs/project/adversarial-review-playbook.md IN FULL.

It is the distilled record of six real, merge-blocking defects found on this exact code path,
on six consecutive changes. It names eight defect classes, and for each one gives:
the TELL (what to scan for), WHERE it lives in this repo, HOW TO CONFIRM it, and the SPECIMEN.
It also lists what does NOT count as a refutation.

The one fact that generates most of them: AN OPEN LEASE IS A CHARGE AND A CAPACITY CLAIM.
pkg/funding.Evaluate derives the funding class from OPEN leases; a lease nobody closes bills a
budget forever and holds GPUs forever, silently. Nothing crashes. Nothing turns red.

Two facts that make confirmation cheap, and you are expected to use them:
  - The engine is PURE. controllers.RunController does no I/O. ClusterState + a static clock IS a
    simulator. Turn any hypothesis into a compiled, running test. DO NOT SPECULATE WHEN YOU CAN EXECUTE.
  - There is one choke point: Bridge.WithWorld (controllers/kube/bridge.go:101), which already
    deep-copies a before-snapshot. Before/after comparisons are free there.
`

const REPORT_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['summary', 'answers', 'evidence', 'findings'],
  properties: {
    // The shaper sets this when the agent's prose does not support a required field.
    // It must never invent one. A non-empty list invalidates the report.
    unsupported: { type: 'array', items: { type: 'string' } },
    summary: { type: 'string', minLength: 200 },
    answers: {
      type: 'array',
      items: {
        type: 'object', additionalProperties: false,
        required: ['question', 'conclusion'],
        properties: {
          question: { type: 'string', minLength: 3 },
          conclusion: { type: 'string', minLength: 80 },
        },
      },
    },
    evidence: {
      type: 'array',
      items: {
        type: 'object', additionalProperties: false,
        required: ['file', 'line', 'quote'],
        properties: {
          file: { type: 'string', minLength: 3 },
          line: { type: 'integer' },
          quote: { type: 'string', minLength: 10 },
        },
      },
    },
    findings: {
      type: 'array',
      items: {
        type: 'object', additionalProperties: false,
        required: ['title', 'severity', 'file', 'line', 'failure_scenario'],
        properties: {
          title: { type: 'string', minLength: 20 },
          severity: { type: 'string', enum: ['critical', 'high', 'medium', 'low'] },
          file: { type: 'string' },
          line: { type: 'integer' },
          failure_scenario: { type: 'string', minLength: 120 },
          taxonomyClass: { type: 'string' },
        },
      },
    },
  },
}

const ATTEST_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['allQuotesFound', 'detail'],
  properties: {
    allQuotesFound: { type: 'boolean' },
    detail: { type: 'string', minLength: 40 },
    missing: { type: 'array', items: { type: 'string' } },
  },
}

const VERDICT_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['refuted', 'reasoning'],
  properties: {
    refuted: { type: 'boolean' },
    reasoning: { type: 'string', minLength: 120 },
    // REPRODUCTION lens. `ranCode` is the honesty flag: a skeptic that argued instead
    // of executing must say so, and its "not a defect" is then worth much less.
    reproduced: { type: 'boolean' },
    ranCode: { type: 'boolean' },
    reproduction: { type: 'string' },
    // CONSEQUENCE lens. A finding can be REAL and its proposed fix a REAPER. That is a
    // distinct verdict, and a 3-way vote has no way to express it.
    fixIsReaper: { type: 'boolean' },
    preExisting: { type: 'boolean' },
    unsupported: { type: 'array', items: { type: 'string' } },
  },
}

const LEADS_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['leads', 'summary'],
  properties: {
    summary: { type: 'string', minLength: 150 },
    leads: {
      type: 'array',
      items: {
        type: 'object', additionalProperties: false,
        required: ['taxonomyClass', 'file', 'line', 'why'],
        properties: {
          taxonomyClass: { type: 'string' },
          file: { type: 'string' },
          line: { type: 'integer' },
          why: { type: 'string', minLength: 40 },
        },
      },
    },
  },
}

const PLACEHOLDER = /^(test|tests|todo|tbd|n\/?a|none|null|nil|a|b|c|x|xxx|foo|bar|baz|placeholder|lorem ipsum)\.?$/i

function degenerate(s, min) {
  const t = (s || '').trim()
  if (t.length < min) return true
  if (PLACEHOLDER.test(t)) return true
  // A single character repeated, or one word padded with punctuation.
  if (/^(.)\1*$/.test(t.replace(/\s/g, ''))) return true
  return false
}

// Returns null when valid, else the reason the report is rejected.
function invalidReason(report, questions) {
  if (!report) return 'the agent produced no output at all'
  if ((report.unsupported || []).length) {
    return `the shaper could not support these required fields from the agent's own prose: ${report.unsupported.join(', ')}. It is forbidden to invent them.`
  }
  if (degenerate(report.summary, 200)) return `summary is degenerate or too short: ${JSON.stringify((report.summary || '').slice(0, 60))}`
  const answers = report.answers || []
  if (answers.length < questions.length) {
    return `answered ${answers.length} of ${questions.length} assigned questions; every question needs an entry`
  }
  for (const a of answers) {
    if (degenerate(a.conclusion, 80)) return `question ${JSON.stringify(a.question.slice(0, 40))} has a degenerate conclusion`
  }
  const ev = report.evidence || []
  if (ev.length < MIN_EVIDENCE) return `only ${ev.length} evidence citations; ${MIN_EVIDENCE} required`
  for (const e of ev) {
    if (degenerate(e.quote, 10)) return `evidence quote for ${e.file}:${e.line} is degenerate`
  }
  for (const f of report.findings || []) {
    if (degenerate(f.title, 20)) return `a finding has a degenerate title: ${JSON.stringify((f.title || '').slice(0, 40))}`
    if (degenerate(f.failure_scenario, 120)) return `finding ${JSON.stringify(f.title.slice(0, 40))} has a degenerate failure_scenario`
  }
  return null
}

// ---------------------------------------------------------------------------
// SHAPE THE OUTPUT, NEVER THE SUBSTANCE.
//
// Forcing a thinking model to emit JSON while it reasons is how four skeptics died on
// 2026-07-09 with `StructuredOutput retry cap exceeded`. Three of them had ALREADY
// REACHED A VERDICT — visible in their last tool call — and lost it to schema
// validation. One of those was the only dissenting vote on its finding.
//
// So: the investigator writes PROSE. A cheap model maps the prose onto the schema.
//
// The line that must not be crossed: the shaper repairs the SHAPE. It never repairs
// the SUBSTANCE. If it may invent, then a lens that did no work — `summary: "test"`,
// a finding titled "a" — gets laundered into a well-formed report, the validator
// passes, and the fail-closed rail says GREEN with a straight face. That is strictly
// worse than the bug this harness exists to catch.
//
// Three things keep it honest:
//   1. The shaper is forbidden to add, infer, complete or invent, and must report
//      `unsupported` rather than fabricate. A non-empty `unsupported` is NOT a pass —
//      it falls through to the retry, and then to BLOCKED.
//   2. `invalidReason` still runs on the shaped object. Degenerate substance blocks.
//   3. The Attest phase independently opens every cited file and checks the quote.
//      A shaper cannot invent evidence without being caught by machinery that already
//      exists and does not trust it.
const SHAPE_RULES = `
You are a FORMATTER, not an analyst. You are given an investigator's verbatim prose and
a JSON schema. Map the prose onto the schema.

ABSOLUTE RULES:
- You may NOT add, infer, complete, summarize-beyond-the-text, or invent ANYTHING. Every
  string you emit must be supported by text that is already present.
- Do NOT improve the analysis. Do NOT fix its reasoning. Do NOT make a weak finding
  sound strong, or a strong one sound hedged.
- Copy evidence quotes EXACTLY as written, character for character. An independent agent
  will open the files and check them.
- If a REQUIRED field has no support in the prose, put its name in the 'unsupported'
  array and leave the field at its most minimal legal value. Do not guess it.
  It is far better to report a field as unsupported than to fabricate one.
- If the prose is empty, a placeholder, or plainly did no work, say so via 'unsupported'.
  Do not manufacture the appearance of work.
`

// think() runs an investigator on prose, then shapes the prose into the schema.
// Returns { obj, raw } or null. `obj` may carry a non-empty `unsupported`, which the
// caller must treat as invalid.
async function think(prompt, schema, opts) {
  let raw = null
  try {
    raw = await agent(prompt, { label: opts.label, phase: opts.phase, model: opts.model, effort: opts.effort })
  } catch (err) {
    return null
  }
  if (typeof raw !== 'string' || raw.trim().length < 80) return null

  let obj = null
  try {
    obj = await agent(
      `${SHAPE_RULES}\n\nSCHEMA:\n${JSON.stringify(schema)}\n\nINVESTIGATOR'S OUTPUT (verbatim, between the markers):\n<<<BEGIN\n${raw}\nEND>>>`,
      { label: `shape:${opts.label}`, phase: opts.phase, model: 'sonnet', effort: 'low', schema })
  } catch (err) {
    return null
  }
  if (!obj) return null
  return { obj, raw }
}

// ---------------------------------------------------------------------------
// THE STANDARD LENSES.
//
// These run on EVERY review of this path, whatever the caller asked for. They are
// keyed to the playbook's taxonomy. A caller cannot remove them: forgetting to ask
// the lease-lifecycle question is precisely how five of the six defects reached a
// reviewer, and an obligation the caller may forget is not an obligation.
// ---------------------------------------------------------------------------
const STANDARD_LENSES = [
  {
    name: 'std:ledger-lifecycle',
    model: 'opus', effort: 'xhigh',
    prompt: `TASK: hunt for an IMMORTAL LEASE (playbook class 1) and CLONED OBLIGATIONS (class 7).
An open lease bills a budget and holds GPUs forever. Nothing crashes when one leaks.
Assume the author left one. Trace every exit path of every function the diff touches.`,
    questions: [
      'Enumerate EVERY exit path (return, continue, break, panic, early error) of every function this change adds or modifies. For each, list which leases are open at that moment and which are closed. Name any path that exits with a lease open that should have been closed.',
      'Does the change add a new CALLER to an existing function whose lease-closing behaviour was only correct for its old callers? failRun once carried the comment "It never holds leases at this point" — true when written, falsified by a new caller. Check every function the diff newly calls.',
      'Does the change set a run to a terminal phase (Failed / Completed) anywhere? For each such site, prove that every lease referencing that run is closed on the same path or by a sweep that provably runs. Cite the sweep.',
      'Does the change write Lease.Status.Closed anywhere other than closeLease()? Every clone of the closing obligation drifts. Name each site and say what closeLease does that the clone does not (events, metrics, ClosureReason).',
      'Take the single most suspicious path you found. Write a Go test that reaches it, drive Reconcile ~20 times over ~20 simulated hours, and assert the lease is closed. Report the actual test output. If it stays open you have a critical finding; if it closes, say what closed it and cite that code.',
    ],
  },
  {
    name: 'std:order-dependence',
    model: 'opus', effort: 'high',
    prompt: `TASK: hunt for LAST-WRITER-WINS (playbook class 3). An outcome that depends on the order of
state.Leases, or on Go's randomized map iteration, is not a specification — it is a coincidence that
holds in your test run.`,
    questions: [
      'Find every assignment to run.Status.* (Phase, Message, Width, CheckpointDeadline) that happens INSIDE a loop in the changed code. For each, is the write guarded by a comparison against what is already there, or does the last iteration simply win?',
      'Find every `for k := range someMap` in the changed code. Go randomizes map iteration order per process. Does any observable outcome depend on that order?',
      'Apply the metamorphic test: take a scenario the change exercises, SHUFFLE the order of state.Leases, replay, and compare the resulting run statuses and lease closure reasons byte-for-byte. Report whether the outcome is invariant. Run it several times. This is the definitive experiment — actually run it.',
      'If an ordering dependence exists, is the correct fix a deterministic sort, or a COMMUTATIVE fold (a severity lattice, a post-loop sweep)? Argue for one. A deterministic order that happens to give the right answer is still a coincidence.',
    ],
  },
  {
    name: 'std:signal-and-identity',
    model: 'opus', effort: 'xhigh',
    prompt: `TASK: hunt for SIGNAL != REALITY (class 4), IDENTITY COARSENING (class 5), and
GUARD BEFORE PREDICATE (class 6). These are the three ways this codebase has mistaken a proxy for
the thing itself.`,
    questions: [
      'Does the change read any Kubernetes condition, taint, timestamp, or object-absence and treat it as a fact about what is PHYSICALLY running on a machine? For each: if that inference is wrong, WHAT RUNS TWICE? (Two live copies of one distributed-training rank is silent data corruption.) A cordon does not stop a pod; NotReady means the control plane cannot hear the kubelet, not that its containers stopped.',
      'For each such signal: WHO can write that field, and can they lie? A wall-clock timestamp written by the very node whose health is in question is not a trustworthy input. Check the shipped RBAC in deploy/helm/gpu-fleet/templates/rbac.yaml.',
      'Does the change compare a COARSER key than the one that matters? node#ordinal slots vs node names; namespace/name run keys vs bare names; envelope names, which are unique only within a Budget. Find every equality test on an identity and name the finest key that should have been used. Two runs may share a node and never share a slot.',
      'Does the change contain a loop with BOTH a type/role filter (`if ... continue`) AND a match predicate? Check their ORDER. A `continue` placed before the test that decides whether this item is ours will silently suppress items and leave a `handled`/`found` flag false. That is exactly R25.',
      'Grep the changed code and its callers for `strings.Contains(err.Error(), ...)` or any error compared by TEXT. An error matched by its message is a coupling, not an error. Enumerate every error the changed functions can return and confirm each swallow site swallows only a typed sentinel via errors.Is.',
    ],
  },
  {
    name: 'std:test-integrity',
    model: 'opus', effort: 'xhigh',
    prompt: `TASK: determine whether the TESTS IN THIS CHANGE ASSERT THE BUG (playbook class 8).
This is the class that defeats every other rail. A test suite passed for all six prior defects. One
test was literally named "the spare must not be consumed" — that assertion WAS the bug. Another suite
provoked a node-failure swap by running 'kubectl cordon', so a green run proved the corruption worked.
You cannot detect a bad test from inside it. Be the oracle its author was not.`,
    questions: [
      'For each test the change adds or modifies: what is the STIMULUS? How does it provoke the behaviour under test? Is that stimulus legitimate — i.e. does it correspond to something that really happens, and does it really mean what the code takes it to mean? A test whose stimulus is invalid proves nothing about the response. This is the highest-yield question here; answer it per test.',
      'Read each new assertion as a claim about the WORLD, not about the code, and check it against docs/project/quota-semantics.md and docs/concepts/leases.md (which are binding). Is the asserted state actually DESIRABLE? When code and test agree they may merely share the author\'s misconception.',
      'MUTATE THE FIX. Delete the load-bearing line the change adds (the closeLease call, the `handled = true`, the phase guard) in a scratch copy, and re-run the tests that supposedly cover it. If they still pass, the test does not test the fix — report that as a finding. Report the exact commands and output.',
      'Does any test assert on a state that the invariant oracle (pkg/invariant, if present) would reject as illegal? A test fixture that constructs an illegal ClusterState is not evidence about a legal system. If pkg/invariant does not exist yet, state which of its invariants the fixtures would violate.',
      'Did the change MODIFY or DELETE an existing test? For each: was the old test wrong, or is the new test being reshaped to accommodate a defect? Quote the old assertion and the new one, and rule on which describes the correct system. Rewriting a test to make a change pass is how a corruption becomes a specification.',
    ],
  },
]

// ---------------------------------------------------------------------------
phase('Scout')

// "Where to look" should be computed from the diff, not recalled from memory.
const scout = await agent(
  `${RULES}\n${PLAYBOOK}\n${A.context}

TASK: You are the scout. Do NOT judge correctness. Mechanically scan the change for the syntactic
TELLS the playbook names, and hand the reviewers a list of PRIORITY LEADS.

The change under review: run \`${DIFF}\` to see it. Also run \`${DIFF} --stat\` first for the shape.

For each of these tells, grep/scan the CHANGED lines and the functions containing them, and emit a
lead for every hit (a lead is a suspicion, not a finding — over-report rather than under-report):

  class 1 IMMORTAL LEASE
    - a function the diff touches that has >1 return, where at least one return is preceded by
      closeLease/closeRunLeases and at least one is not
    - any assignment of RunPhaseFailed or RunPhaseComplete not followed by a closure call
    - a new call site of an existing lease-closing function
  class 2 COMMENT-AS-ENFORCEMENT
    - added/changed comments containing: never, always, cannot, guaranteed, by construction, callers must
  class 3 LAST-WRITER-WINS
    - an assignment to run.Status.* inside a for loop
    - 'for k := range <map>' where the body writes observable state
  class 4 SIGNAL != REALITY
    - reads of NodeReady, Unschedulable, LastTransitionTime, Taints, Status.Phase on a Node or Pod
  class 5 IDENTITY COARSENING
    - use of nodeFromSlot(), or any comparison of node names where a node#ordinal slot exists
  class 6 GUARD BEFORE PREDICATE
    - a loop containing both a 'continue' role/type filter and a match predicate — report their order
    - any 'handled' / 'found' / 'ok' boolean flag
    - strings.Contains(err.Error(), ...) anywhere on the path
  class 7 CLONED OBLIGATION
    - any write to '.Status.Closed' outside func closeLease in a non-test file
  class 8 TEST ASSERTS THE BUG
    - every _test.go file the diff modifies or deletes lines from (deletions especially)

Report each lead with its taxonomyClass, file, line, and a one-sentence 'why'. Include leads even if
you strongly suspect they are benign — a reviewer will adjudicate. If a class has NO hits, do not
invent one; just omit it, and say so in the summary.

If the diff command produces nothing, say so loudly in the summary — a review of an empty diff must
not read as clean.`,
  { label: 'scout:tells', phase: 'Scout', model: 'sonnet', effort: 'medium', schema: LEADS_SCHEMA })

const leads = scout?.leads || []
if (!scout) {
  log('SCOUT DIED — reviewers proceed without computed leads; this is recorded in the verdict.')
} else {
  log(`scout: ${leads.length} priority lead(s) across ${new Set(leads.map(l => l.taxonomyClass)).size} taxonomy class(es)`)
}

const leadBlock = leads.length
  ? `\nPRIORITY LEADS — a scout mechanically scanned the diff for the playbook's tells and flagged these.
They are SUSPICIONS, not findings. Adjudicate each one that falls in your lens. Do not assume a prior
reviewer cleared it. Absence from this list is not clearance either — the scout only greps.\n` +
    leads.map(l => `  [${l.taxonomyClass}] ${l.file}:${l.line} — ${l.why}`).join('\n') + '\n'
  : (scout
    ? '\nThe scout found no syntactic tells in this diff. That is weak evidence at best: the scout only greps, and five of the six historical defects were OMISSIONS, which no grep can see.\n'
    : '\nWARNING: the scout agent died. You have no computed leads. Scan for the playbook tells yourself.\n')

// ---------------------------------------------------------------------------
phase('Review')

async function runLens(lens) {
  const questions = lens.questions || []
  const qBlock = questions.length
    ? `\nASSIGNED QUESTIONS — you must return one 'answers' entry per question, using the question text as the key:\n${questions.map((q, i) => `  ${i + 1}. ${q}`).join('\n')}\n`
    : ''
  let rejection = ''
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const prompt = `${RULES}\n${PLAYBOOK}\n${A.context}\n${leadBlock}\n${lens.prompt}\n${qBlock}` +
      `\nThe change under review: \`${DIFF}\`. Tag each finding with its playbook 'taxonomyClass' when one fits.\n` +
      (rejection ? `\n\nYOUR PREVIOUS ATTEMPT WAS REJECTED: ${rejection}\nDo the work this time. Read the code. Cite it.` : '')
    const shaped = await think(prompt, REPORT_SCHEMA, {
      label: attempt === 1 ? lens.name : `${lens.name}:retry${attempt - 1}`,
      phase: 'Review',
      model: lens.model || 'opus',
      effort: lens.effort || 'high',
    })
    const report = shaped ? shaped.obj : null
    const why = invalidReason(report, questions)
    if (!why) return { lens: lens.name, report, attempts: attempt }
    rejection = why
    log(`lens "${lens.name}" attempt ${attempt} REJECTED: ${why}`)
  }
  // Fail closed. This lens did not do the work; it must not read as green.
  return { lens: lens.name, report: null, blocked: true, reason: rejection, attempts: MAX_ATTEMPTS }
}

// Standard lenses are prefixed 'std:' so a caller lens can never collide with, and
// thereby silently replace, one of them.
const ALL_LENSES = [...STANDARD_LENSES, ...(Array.isArray(A.lenses) ? A.lenses : [])]
log(`running ${STANDARD_LENSES.length} standard + ${ALL_LENSES.length - STANDARD_LENSES.length} caller lens(es)`)

const lensResults = await parallel(ALL_LENSES.map(l => () => runLens(l)))

// ---------------------------------------------------------------------------
phase('Attest')

// Independently verify each surviving lens's citations against the real files.
// An agent can claim it read the code; it cannot fake a quote that isn't there.
const attested = await pipeline(
  lensResults.filter(r => r && !r.blocked),
  async (r) => {
    const ev = r.report.evidence
    const listing = ev.map((e, i) => `${i + 1}. ${e.file}:${e.line}\n   QUOTE: ${JSON.stringify(e.quote)}`).join('\n')
    const check = await agent(
      `${RULES}\n\nTASK: verify citations. For each item below, open the file and check whether the quoted text
appears in it, at or near the stated line (±15 lines is fine; whitespace differences are fine).
Do NOT judge whether the claim is correct — only whether the quote is really in the file.

${listing}

Set allQuotesFound=false if ANY quote cannot be found, and list those in 'missing' as "file:line".`,
      { label: `attest:${r.lens}`, phase: 'Attest', model: 'sonnet', effort: 'low', schema: ATTEST_SCHEMA })

    if (!check) return { ...r, blocked: true, reason: 'citation attestation agent died; cannot confirm the lens did the work' }
    if (!check.allQuotesFound) {
      return { ...r, blocked: true, reason: `fabricated or unlocatable citations: ${(check.missing || []).join(', ')}` }
    }
    return { ...r, attested: true }
  }
)

const blocked = lensResults.filter(r => r && r.blocked).concat(attested.filter(r => r && r.blocked))
const good = attested.filter(r => r && !r.blocked)

blocked.forEach(b => log(`BLOCKED lens "${b.lens}": ${b.reason}`))
good.forEach(g => log(`lens "${g.lens}": ${g.report.findings.length} finding(s), ${g.report.evidence.length} citations verified`))

const raised = good.flatMap(g => g.report.findings.map(f => ({ ...f, lens: g.lens })))

if (raised.length === 0) {
  return {
    verdict: blocked.length ? 'BLOCKED — a lens produced no verifiable work; this is NOT a green review' : 'GREEN',
    blockedLenses: blocked.map(b => ({ lens: b.lens, reason: b.reason })),
    scoutLeads: leads,
    scoutDied: !scout,
    summaries: good.map(g => ({ lens: g.lens, summary: g.report.summary })),
    confirmed: [], unresolved: [],
  }
}

// ---------------------------------------------------------------------------
phase('Judge')

// THE PANEL IS HETEROGENEOUS, AND THE VOTE IS NOT A VOTE.
//
// Three skeptics on one model, with three prompts, are three samples of ONE
// distribution. They fail together, in correlated ways, because they share a prior.
// That is how a quorum refuted a TRUE finding on 2026-07-09: all three reached for
// "pre-existing, therefore not worsened", which is a valid move that happened to
// conceal that the fix under review was inert in production.
//
// Different models do not share that reflex. But heterogeneous judges are not
// EXCHANGEABLE, and majority voting assumes exchangeability. If the reproducer runs a
// test and the tracer reads the code, counting them 1-1 asserts they are equally
// likely to be right about this question, which is neither true nor well-defined.
//
// So each judge is paired to the lens it is actually good at, and the aggregation is
// asymmetric:
//
//   A REPRODUCTION CONFIRMS, ALONE.      A compiled test that exhibits the bad state
//                                        is a fact. No amount of tracing refutes it.
//   A REFUTATION NEEDS BOTH.             The trace must show the code cannot produce
//                                        it AND the reproduction must have been tried
//                                        and failed. Absence of evidence counts only
//                                        when somebody looked.
//   THE CONSEQUENCE LENS MAY VETO A FIX  without touching the finding. "Real bug,
//                                        proposed fix is a reaper" is a distinct and
//                                        important outcome. It has caught three.
//
// Everything else is UNRESOLVED. Silence is not consent, in either direction.
const SKEPTIC_PANEL = [
  {
    role: 'reproduce', model: 'sonnet', effort: 'high',
    lens: `REPRODUCTION. The engine is pure: controllers.ClusterState plus a static clock IS a simulator,
so an engine claim can always be tested. COPY the repo to a temp dir, write a Go test, RUN it, and paste
the real output. Use a PRODUCTION-SHAPED fixture — a fixture richer than reality proves nothing about
reality. Set ranCode=true only if you actually compiled and ran something. If you did not run anything,
set ranCode=false and say so plainly: do NOT rule refuted on an argument you could have tested.
Set reproduced=true only if you saw the bad state in test output with your own eyes.`,
  },
  {
    role: 'trace', model: 'opus', effort: 'high',
    lens: `CODE TRACE. Does the claimed behaviour follow from the code AS WRITTEN, in the CURRENT tree?
Trace every function on the path and quote each. If a later commit changed it, name the line and say
whether that closes the finding or merely moves it. A fix that is dead code in production is not a fix.
Set refuted=true only if the trace shows the code cannot produce the claimed state.`,
  },
  {
    role: 'consequence', model: 'fable', effort: 'high',
    lens: `CONSEQUENCE AND REAPER-CHECK. First: assume it is real. Name the budget charged, the GPUs held,
and for how long. Then INVERT: if we "fixed" this, what LEGAL state would the fix destroy?
pkg/invariant's package doc lists four invariants rejected for exactly that reason — slot
oversubscription is tolerated, a cordoned node's leases are healthy, a spare-only run is legal, a Running
run mid-swap holds zero active leases. Set fixIsReaper=true if the obvious repair would destroy healthy
work, and say which state it kills. A fix that reaps a healthy run is worse than the bug it repairs.
You may confirm the finding AND veto its fix; those are different questions.`,
  },
]

// adjudicate applies the asymmetric rule above to whatever votes survived.
function adjudicate(f, votes) {
  const valid = votes.filter(Boolean)
  const byRole = {}
  valid.forEach(v => { byRole[v.role] = v })
  const repro = byRole.reproduce
  const trace = byRole.trace
  const cons = byRole.consequence
  const reaperWarning = cons && cons.fixIsReaper ? cons.reasoning : null

  // A reproduction confirms, alone.
  if (repro && repro.reproduced === true) {
    return { finding: f, status: 'confirmed', why: 'reproduced by a compiled, running test', reaperWarning, votes: valid }
  }
  if (valid.length < SKEPTIC_QUORUM) {
    return { finding: f, status: 'unresolved', why: `only ${valid.length}/${SKEPTIC_QUORUM} skeptics returned a usable verdict`, reaperWarning, votes: valid }
  }

  const traceRefutes = trace && trace.refuted === true
  const reproTriedAndFailed = repro && repro.ranCode === true && repro.reproduced === false

  if (traceRefutes && reproTriedAndFailed) {
    return { finding: f, status: 'refuted', preExisting: !!trace.preExisting, reaperWarning, votes: valid }
  }
  if (traceRefutes && repro && repro.ranCode !== true) {
    return {
      finding: f, status: 'unresolved', reaperWarning, votes: valid,
      why: 'the trace refutes it, but nobody ran the code. Absence of evidence counts only when somebody looked.',
    }
  }
  // Fail closed: not refuted by BOTH the trace and a failed reproduction.
  return { finding: f, status: 'confirmed', why: 'not refuted by both the trace and a failed reproduction', reaperWarning, votes: valid }
}

const judged = await pipeline(
  raised,
  f => parallel(SKEPTIC_PANEL.map(sk => async () => {
    const shaped = await think(
      `${RULES}\n${PLAYBOOK}\n${A.context}

A reviewer raised this finding. Your job is to REFUTE it. Default to refuted=true when uncertain —
only defects we are sure of are worth acting on. But do NOT refute by silence or hand-waving: give a
concrete reason grounded in the code.

Write PROSE. Do not produce JSON. Another agent will map what you write onto a schema, and it is
forbidden to invent anything you did not say — so state every conclusion explicitly, including whether
you ran code, whether it reproduced, and whether the obvious fix would be a reaper.

THESE ARE NOT VALID REFUTATIONS (offering one is a task failure):
  - "the test suite passes" — it passed for all seven prior defects, and one suite ASSERTED the bug.
  - "the comment says it cannot happen" — a comment is an assertion nothing runs.
  - "it would take an unusual sequence of events" — this scheduler runs for months. Node failures,
    cordons, budget-window rollovers and controller restarts are all routine.
  - "it is pre-existing" — a classification, not a refutation. Set preExisting=true, explain, and
    refute ONLY if the change also leaves its consequences and its REACHABILITY unchanged. Making a
    dead path reachable IS worsening it. And a change that fails to achieve its stated purpose is a
    finding in its own right, whatever the code did before.

FINDING: ${f.title}
CLASS: ${f.taxonomyClass || '(untagged)'}
SEVERITY CLAIMED: ${f.severity}
LOCATION: ${f.file}:${f.line}
FAILURE SCENARIO: ${f.failure_scenario}

Your lens — ${sk.lens}`,
      VERDICT_SCHEMA,
      { label: `judge:${sk.role}:${f.title.slice(0, 20)}`, phase: 'Judge', model: sk.model, effort: sk.effort })

    if (!shaped) return null                                   // a dead skeptic is NOT a vote
    const v = shaped.obj
    if ((v.unsupported || []).length) return null              // nor is a fabricated one
    if (degenerate(v.reasoning, 120)) return null
    return { ...v, role: sk.role }
  })).then(votes => adjudicate(f, votes))
)

const confirmed = judged.filter(Boolean).filter(j => j.status === 'confirmed')
const unresolved = judged.filter(Boolean).filter(j => j.status === 'unresolved')
const refuted = judged.filter(Boolean).filter(j => j.status === 'refuted')

log(`${confirmed.length} confirmed, ${unresolved.length} UNRESOLVED (under quorum), ${refuted.length} refuted, ${blocked.length} blocked lens(es)`)

let verdict = 'GREEN'
if (blocked.length) verdict = 'BLOCKED — a lens produced no verifiable work; this is NOT a green review'
else if (confirmed.length) verdict = 'DEFECTS CONFIRMED'
else if (unresolved.length) verdict = 'UNRESOLVED — findings could not be adjudicated; do not treat as green'

return {
  verdict,
  blockedLenses: blocked.map(b => ({ lens: b.lens, reason: b.reason })),
  scoutLeads: leads,
  scoutDied: !scout,
  confirmed: confirmed.map(j => ({
    ...j.finding,
    why: j.why,
    // A real finding whose obvious fix is a reaper. Read this before writing the fix.
    reaperWarning: j.reaperWarning || null,
    votes: j.votes.map(v => ({ role: v.role, refuted: v.refuted, ranCode: !!v.ranCode, reasoning: v.reasoning })),
  })),
  unresolved: unresolved.map(j => ({ ...j.finding, why: j.why, reaperWarning: j.reaperWarning || null })),
  // Refuted-as-pre-existing is surfaced, never buried: a defect the change did not
  // introduce is still a defect, and someone must decide to file it.
  refuted: refuted.map(j => ({ title: j.finding.title, preExisting: !!j.preExisting, reaperWarning: j.reaperWarning || null })),
  summaries: good.map(g => ({ lens: g.lens, summary: g.report.summary })),
}

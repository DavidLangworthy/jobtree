export const meta = {
  name: 'adversarial-review',
  description: 'Fail-closed adversarial review: a lens that produces no real work BLOCKS, it never reads as green',
  whenToUse: 'Reviewing any change to the funding engine, the scheduler plugin, or another sole-committer path. Pass args: {context, commit, lenses:[{name,model,effort,prompt,questions}], minEvidence, skepticQuorum, refuteThreshold}.',
  phases: [
    { title: 'Review', detail: 'each lens investigates, cites evidence, and is validated' },
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
// ---------------------------------------------------------------------------

const A = args || {}
if (!A.context || !Array.isArray(A.lenses) || A.lenses.length === 0) {
  throw new Error('adversarial-review requires args: {context, lenses:[{name,prompt,questions}]}')
}
const MIN_EVIDENCE = A.minEvidence || 3
const SKEPTIC_QUORUM = A.skepticQuorum || 3
const REFUTE_THRESHOLD = A.refuteThreshold || 2
const MAX_ATTEMPTS = 3

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

const REPORT_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['summary', 'answers', 'evidence', 'findings'],
  properties: {
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
    reproduced: { type: 'string' },
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
phase('Review')

async function runLens(lens) {
  const questions = lens.questions || []
  const qBlock = questions.length
    ? `\nASSIGNED QUESTIONS — you must return one 'answers' entry per question, using the question text as the key:\n${questions.map((q, i) => `  ${i + 1}. ${q}`).join('\n')}\n`
    : ''
  let rejection = ''
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const prompt = `${RULES}\n${A.context}\n\n${lens.prompt}\n${qBlock}` +
      (rejection ? `\n\nYOUR PREVIOUS ATTEMPT WAS REJECTED: ${rejection}\nDo the work this time. Read the code. Cite it.` : '')
    let report = null
    try {
      report = await agent(prompt, {
        label: attempt === 1 ? lens.name : `${lens.name}:retry${attempt - 1}`,
        phase: 'Review',
        model: lens.model || 'opus',
        effort: lens.effort || 'high',
        schema: REPORT_SCHEMA,
      })
    } catch (err) {
      report = null
    }
    const why = invalidReason(report, questions)
    if (!why) return { lens: lens.name, report, attempts: attempt }
    rejection = why
    log(`lens "${lens.name}" attempt ${attempt} REJECTED: ${why}`)
  }
  // Fail closed. This lens did not do the work; it must not read as green.
  return { lens: lens.name, report: null, blocked: true, reason: rejection, attempts: MAX_ATTEMPTS }
}

const lensResults = await parallel(A.lenses.map(l => () => runLens(l)))

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
    summaries: good.map(g => ({ lens: g.lens, summary: g.report.summary })),
    confirmed: [], unresolved: [],
  }
}

// ---------------------------------------------------------------------------
phase('Judge')

const SKEPTIC_LENSES = [
  'CODE TRACE: does the claimed behavior follow from the code as written? Trace every function on the path, quoting it. If the trace does not produce the claimed outcome, refute.',
  'REPRODUCTION: write the scenario as a real test in a scratch copy and RUN it. Do not add files to the repo. If it does not reproduce, refute.',
  'SCOPING: is the state reachable through the shipped control flow? And does it behave DIFFERENTLY before this change? If the pre-change code produces the same bad outcome and this change does not worsen its consequences, refute as pre-existing (but say so explicitly).',
  'SEVERITY: assume it is real — is the stated failure scenario actually what happens, or is the impact narrower/wider than claimed?',
]

const judged = await pipeline(
  raised,
  f => parallel(Array.from({ length: SKEPTIC_QUORUM }, (_, i) => async () => {
    try {
      const v = await agent(
        `${RULES}\n${A.context}

A reviewer raised this finding. Your job is to REFUTE it. Default to refuted=true when uncertain —
only defects we are sure of are worth acting on. But do NOT refute by silence or hand-waving: give a
concrete reason grounded in the code.

FINDING: ${f.title}
SEVERITY CLAIMED: ${f.severity}
LOCATION: ${f.file}:${f.line}
FAILURE SCENARIO: ${f.failure_scenario}

Your lens — ${SKEPTIC_LENSES[i % SKEPTIC_LENSES.length]}

Return refuted=true only if this is not a real defect introduced or worsened by the change.`,
        { label: `judge${i}:${f.title.slice(0, 24)}`, phase: 'Judge', model: 'opus', effort: 'high', schema: VERDICT_SCHEMA })
      if (!v || degenerate(v.reasoning, 120)) return null // not a vote
      return v
    } catch (err) {
      return null // a dead skeptic is NOT a refutation
    }
  })).then(votes => {
    const valid = votes.filter(Boolean)
    // Quorum first: silence is not consent in EITHER direction. Without a full
    // quorum we cannot conclude the finding was refuted, so it stays on the table.
    if (valid.length < SKEPTIC_QUORUM) {
      return { finding: f, status: 'unresolved', reason: `only ${valid.length}/${SKEPTIC_QUORUM} skeptics returned a usable verdict`, votes: valid }
    }
    const refutations = valid.filter(v => v.refuted).length
    if (refutations >= REFUTE_THRESHOLD) return { finding: f, status: 'refuted', votes: valid }
    return { finding: f, status: 'confirmed', votes: valid.filter(v => !v.refuted) }
  })
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
  confirmed: confirmed.map(j => ({ ...j.finding, evidence: j.votes.map(v => v.reproduced || v.reasoning) })),
  unresolved: unresolved.map(j => ({ ...j.finding, why: j.reason })),
  refutedTitles: refuted.map(j => j.finding.title),
  summaries: good.map(g => ({ lens: g.lens, summary: g.report.summary })),
}

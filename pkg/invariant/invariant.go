// Package invariant is the oracle the test author did not write.
//
// Six consecutive changes to the funding and node-failure paths each shipped a
// real defect past a green test suite. One of those tests was named "the spare
// must not be consumed" — that assertion WAS the bug. Another suite provoked a
// node-failure swap by cordoning a node, so a green run was proof the corruption
// worked.
//
// You cannot detect a defective test from inside the test. It is internally
// consistent: it asserts a state, and the state obtains. The only detector is an
// oracle the test author did not write, applied to every state every test
// produces. That is this package.
//
// It is hooked into the end of every engine entry point (see
// controllers/invariants.go). Under `go test` a violation PANICS, so a test that
// asserts an illegal state goes red inside the call, before its own assertions
// run. In production a violation is logged and counted, never fatal: an oracle
// that crashes the scheduler is worse than the bug it found.
//
// Green stops meaning "asserted" and starts meaning "asserted and legal".
//
// # On what is NOT here
//
// Four plausible invariants were considered and REJECTED because each is false
// in a state this engine legally produces. They are recorded in
// docs/project/adversarial-review-playbook.md and repeated here because the next
// person to read this file will want to add them:
//
//   - "no two open leases hold the same node#ordinal slot". The engine
//     deliberately tolerates oversubscription and declines a swap rather than
//     evict a funded conflict (controllers/run_controller.go:1248).
//   - "a lease naming a node absent from the cluster is an orphan". The bridge
//     drops UNUSABLE nodes from its snapshot, and a CORDONED node is unusable.
//     This invariant would close the healthy, running leases of a cordoned node:
//     it is the R21 corruption rebuilt.
//   - "an open Spare lease implies an open Active lease of the same group". A
//     leftover spare-only run is an explicitly named, legal state
//     (controllers/run_controller.go:246), and the plugin mints per pod, so a
//     spare can exist before its actives.
//   - "a Running run holds at least one open active lease". False during a swap:
//     HandleNodeFailure closes the failed active AND the spare, emits a swap
//     POD, and sets Running — the replacement lease is minted later, by the
//     plugin, at PreBind. For a single-group run there are zero open active
//     leases at return, and Running is correct.
//
// An invariant that is wrong is not a weaker safety net. It is a reaper.
package invariant

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Invariant IDs. Stable strings: they appear in metrics, logs, and test output.
const (
	// CloseStamped: a closure is a complete fact, or it silently under-charges.
	// pkg/funding's effectiveEnd falls back to the lease's START instant when a
	// closed lease carries no Ended timestamp, so a half-closed lease accrues
	// nothing at all for its entire life.
	CloseStamped = "INV-CLOSE-STAMPED"

	// TerminalPresent: a terminally Failed or Completed run holds no open lease.
	// An open lease charges a budget and holds GPUs forever; a terminal run will
	// never close it, because nothing reconciles a corpse. This is the
	// immortal-lease class.
	//
	// It holds only on RETURN of an engine entry point. Mid-method it is
	// legitimately false: HandleNodeFailure marks a run Failed inside its lease
	// loop and sweeps the run's remaining leases only after the loop, precisely
	// so the outcome cannot depend on lease order.
	TerminalPresent = "INV-TERMINAL-PRESENT"

	// TerminalNoPods: a terminally Failed or Completed run has no pods left.
	//
	// This is INV-TERMINAL-PRESENT told backwards, and it needs its own name because
	// the ledger cannot see it. Bridge.apply deletes exactly the pods ABSENT from
	// State.Pods, so a pod the engine forgot to drop keeps running, keeps holding the
	// GPU the ledger has just handed back, and the engine happily plans new work onto
	// it — work the kube-scheduler can then never bind.
	//
	// Every failure path in this repo closed the leases and left the containers, for
	// as long as the file existed. controllers.releaseRun is the answer; this is the
	// rail that says so.
	//
	// Like TerminalPresent it holds on RETURN only: HandleNodeFailure marks a run
	// Failed inside its lease loop and sweeps both planes after it.
	TerminalNoPods = "INV-TERMINAL-NO-PODS"

	// WidthAssembled: an assembled Running run holds at least its minimum
	// runnable width, counting EVERY open non-spare lease. "Start together or not
	// at all" — a fixed-width gang missing a rank makes no progress while every one
	// of its surviving containers charges a budget.
	//
	// Gated on AwaitingMint. The controller is not the committer of leases: it
	// emits a pod, and the scheduler plugin mints the lease at PreBind. Between
	// those two moments a healthy run legally holds less width than it reports.
	WidthAssembled = "INV-WIDTH-ASSEMBLED"

	// GroupStamped: every OPEN lease names the placement group it belongs to.
	//
	// pkg/resolver buckets a run's leases by it to cut ONE group rather than the whole
	// run; the elastic loop shrinks by it; a node-failure swap pairs a dead rank with
	// the spare of its own group by it. A lease with no group index makes all three
	// address the wrong thing, and none of them can tell.
	//
	// Before R28b the sole committer stamped none, and three separate consumers
	// papered over it with a "0" default. The result looked correct because every pod
	// was stamped "0" too. This invariant is why that cannot come back.
	GroupStamped = "INV-GROUP-STAMPED"

	// ClosedMonotone: a Lease is an immutable fact. Its Spec never changes, its
	// closure never reverts, and its recorded ending never moves. A reopened or
	// re-stamped lease makes funding.Evaluate double-count a settled fact.
	ClosedMonotone = "INV-CLOSED-MONOTONE"

	// LeaseHasPod: a run holding an open lease holds at least one pod.
	//
	// The oracle counts leases, and so it was structurally blind to the defect that
	// the fix for the immortal-lease class introduced: reclaimSquatter closed ONE
	// lease and deleted the victim's WHOLE pod set, leaving the victim Running with
	// open leases and no containers. Every lease-side invariant above was satisfied.
	//
	// The two planes only ever move in one legal direction apart. A pod appears
	// FIRST and the plugin mints its lease at PreBind, so "pods without leases" is
	// the ordinary swap window, and the checkpoint grace parks a run with its
	// group's lease closed and its containers deliberately alive. The reverse — an
	// open lease with no container anywhere in the run — is nothing but a budget
	// billing for a GPU that is doing no work.
	//
	// This is the COARSE form of the rule: it asks whether the run has any pod at
	// all, not whether each lease has its own. The exact per-lease correspondence
	// needs the node and slot of both planes, and it belongs in R26's ledger
	// auditor, which sees them. The coarse form is what a state projection can
	// honestly answer, and it is enough to catch a whole-pod-set deletion.
	LeaseHasPod = "INV-LEASE-HAS-POD"

	// PhaseDerived: status.phase must equal what status.conditions derive (R11).
	//
	// Phase is what every control path keys off — the completion gate, the elastic
	// loop, the resolver, the CLI — and conditions are what operators and
	// `kubectl wait` key off. If they can disagree, one of the two audiences is
	// being lied to, and which one depends on where the bug is. SetRunState makes
	// them agree by construction, so any violation here means something wrote
	// status.phase behind its back.
	PhaseDerived = "INV-PHASE-DERIVED"
)

// Violation is one broken invariant, named so it can be grepped, counted, and
// cited in a review.
type Violation struct {
	ID      string
	Subject string
	Detail  string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s: %s", v.ID, v.Subject, v.Detail)
}

// Lease is the projection of a v1.Lease this package reasons about. The
// controller builds it; this package never imports the API types, so it cannot
// drift into a second, competing view of them.
type Lease struct {
	Name          string
	RunKey        string
	Closed        bool
	GroupIndex    string
	HasEnded      bool
	EndedUnixNano int64
	ClosureReason string
	// SpecFingerprint is a deterministic encoding of the lease's Spec. Two
	// leases with equal fingerprints have equal specs.
	SpecFingerprint string
}

// Run is the projection of a v1.Run this package reasons about.
type Run struct {
	Key   string
	Phase string
	// Terminal is true for the phases from which a run never returns.
	Terminal bool
	// RunnableGPUs is the run's TOTAL live width: every open, non-spare lease,
	// whatever minted it — base gang and elastic grow alike. It is NOT the base
	// gang width.
	//
	// That distinction is a reaper. The resolver's lottery guard permits cutting a
	// malleable run's BASE group while its grow ranks still cover the declared
	// minimum, so a run can legally be Running with zero base-gang GPUs. An
	// invariant that compared base width against a total-GPU minimum would panic
	// on it. (The narrower "a run must not ADOPT to Running on grow leases alone"
	// rule is real, and it lives in the adoption path where it belongs.)
	//
	// Both fields must be computed by the SAME helpers the controller uses to
	// decide, or this package becomes a second implementation of the rule it is
	// checking.
	RunnableGPUs    int
	MinRunnableGPUs int
	// DerivedPhase is what this run's OWN status conditions derive to (R11). It
	// is empty for a run carrying no conditions at all — a hand-built pure-engine
	// fixture that never went through a status write — and such a run is skipped,
	// the same documented blind spot KnownToLedger takes.
	//
	// It is deliberately computed by the API package's DeriveRunPhase, not
	// reimplemented here: an oracle that re-derives the rule it is checking checks
	// only that someone wrote the same bug twice.
	DerivedPhase string
	// Pods is how many pods of any role the workload plane still holds for this
	// run. It is the plane the ledger cannot see; see TerminalNoPods.
	Pods int
	// AwaitingMint is true when the run has an intent pod with no matching open
	// lease: the plugin has not committed it yet. Width invariants do not apply.
	AwaitingMint bool
	// KnownToLedger is true when the ledger has ever heard of this run: it holds
	// at least one Lease (open OR closed) or at least one pod.
	//
	// This is a deliberate, documented blind spot. A run with no lease and no pod
	// has not been placed, so no width statement about it can be checked — and
	// many unit tests legitimately hand-build such a run, setting Phase directly
	// to exercise follow/completion logic that never touches the ledger. Note the
	// discriminator is "no leases AT ALL", not "no OPEN leases": a run that lost
	// every lease it ever held still has CLOSED ones, so it stays checked. That
	// is the case worth catching — a run reporting Running over a dead gang.
	KnownToLedger bool
}

// World is a complete engine state, projected.
type World struct {
	Runs   []Run
	Leases []Lease
}

// CheckSteady returns every steady-tier invariant violated by w. Steady
// invariants must hold on RETURN of every engine entry point — never mid-method.
func CheckSteady(w World) []Violation {
	var out []Violation

	openByRun := map[string][]string{}
	for _, l := range w.Leases {
		if l.Closed {
			if !l.HasEnded || l.ClosureReason == "" {
				out = append(out, Violation{
					ID:      CloseStamped,
					Subject: "lease " + l.Name,
					Detail: fmt.Sprintf(
						"closed but incompletely stamped (hasEnded=%t closureReason=%q); "+
							"funding.effectiveEnd will bill it to its START instant, accruing nothing",
						l.HasEnded, l.ClosureReason),
				})
			}
			continue
		}
		if l.GroupIndex == "" {
			out = append(out, Violation{
				ID:      GroupStamped,
				Subject: "lease " + l.Name,
				Detail: "open but names no placement group; the resolver, the elastic loop and the " +
					"node-failure swap all address work by it, and all three would silently address " +
					"the wrong ranks",
			})
		}
		openByRun[l.RunKey] = append(openByRun[l.RunKey], l.Name)
	}

	for _, r := range w.Runs {
		if r.DerivedPhase != "" && r.DerivedPhase != r.Phase {
			out = append(out, Violation{
				ID:      PhaseDerived,
				Subject: "run " + r.Key,
				Detail: fmt.Sprintf(
					"status.phase is %q but its own status.conditions derive %q; "+
						"the control paths and `kubectl wait` are being told different things",
					r.Phase, r.DerivedPhase),
			})
		}
		if r.Terminal {
			if names := openByRun[r.Key]; len(names) > 0 {
				sort.Strings(names)
				out = append(out, Violation{
					ID:      TerminalPresent,
					Subject: "run " + r.Key,
					Detail: fmt.Sprintf(
						"terminal (phase=%s) but still holds %d open lease(s): %s — "+
							"each charges its budget and holds its GPUs forever; nothing reconciles a corpse",
						r.Phase, len(names), strings.Join(names, ", ")),
				})
			}
			if r.Pods > 0 {
				out = append(out, Violation{
					ID:      TerminalNoPods,
					Subject: "run " + r.Key,
					Detail: fmt.Sprintf(
						"terminal (phase=%s) but the workload plane still holds %d pod(s) — "+
							"each keeps a GPU the ledger has already handed back, and the engine will plan onto it",
						r.Phase, r.Pods),
				})
			}
			continue
		}
		if names := openByRun[r.Key]; len(names) > 0 && r.Pods == 0 {
			sort.Strings(names)
			out = append(out, Violation{
				ID:      LeaseHasPod,
				Subject: "run " + r.Key,
				Detail: fmt.Sprintf(
					"holds %d open lease(s) (%s) and not one pod — the ledger is billing a budget and "+
						"reserving GPUs for containers that do not exist",
					len(names), strings.Join(names, ", ")),
			})
		}
		if r.Phase != "Running" || r.AwaitingMint || !r.KnownToLedger {
			continue
		}
		if r.RunnableGPUs < r.MinRunnableGPUs {
			out = append(out, Violation{
				ID:      WidthAssembled,
				Subject: "run " + r.Key,
				Detail: fmt.Sprintf(
					"reports Running while holding %d of %d minimum runnable GPUs, with no pod awaiting a mint — "+
						"a gang missing a rank makes no progress while every surviving container charges a budget",
					r.RunnableGPUs, r.MinRunnableGPUs),
			})
		}
	}
	return out
}

// CheckTransition returns every transition-tier invariant violated by the move
// from before to after. Leases absent from before are NEW MINTS and are exempt:
// the bridge legitimately creates and even closes a lease within a single pass,
// and the before-snapshot only holds what existed when the world was loaded.
func CheckTransition(before, after World) []Violation {
	var out []Violation
	prior := make(map[string]Lease, len(before.Leases))
	for _, l := range before.Leases {
		prior[l.Name] = l
	}

	for _, now := range after.Leases {
		was, existed := prior[now.Name]
		if !existed {
			continue
		}
		switch {
		case was.Closed && !now.Closed:
			out = append(out, Violation{
				ID: ClosedMonotone, Subject: "lease " + now.Name,
				Detail: "reopened: a closed Lease is a settled fact, and reopening it makes funding.Evaluate charge it twice",
			})
		case was.SpecFingerprint != now.SpecFingerprint:
			out = append(out, Violation{
				ID: ClosedMonotone, Subject: "lease " + now.Name,
				Detail: "Spec mutated in place; a Lease is immutable once written (slice, payer and interval are the audit record)",
			})
		case was.HasEnded && now.HasEnded && was.EndedUnixNano != now.EndedUnixNano:
			out = append(out, Violation{
				ID: ClosedMonotone, Subject: "lease " + now.Name,
				Detail: fmt.Sprintf("recorded ending moved from %d to %d; a closure timestamp is a fact, not a variable",
					was.EndedUnixNano, now.EndedUnixNano),
			})
		case was.ClosureReason != "" && was.ClosureReason != now.ClosureReason:
			out = append(out, Violation{
				ID: ClosedMonotone, Subject: "lease " + now.Name,
				Detail: fmt.Sprintf("closure reason rewritten from %q to %q; the ledger's why is not editable",
					was.ClosureReason, now.ClosureReason),
			})
		}
	}
	return out
}

// Reporter decides what a violation means. Production logs and counts; tests
// panic.
type Reporter func(site string, violations []Violation)

// Report is the process-wide violation handler. It is set to Panic under `go
// test` and to a caller-supplied reporter in production (see
// controllers/kube.Bridge). It must never be nil.
var Report Reporter = Panic

// UnderTest reports whether this process is a Go test binary. It is the reason
// the oracle needs no opt-in: a test author cannot forget to enable it.
func UnderTest() bool {
	if strings.HasSuffix(os.Args[0], ".test") {
		return true
	}
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-test.") || strings.HasPrefix(arg, "--test.") {
			return true
		}
	}
	return false
}

// EnvMode names the environment variable that downgrades the test-mode reporter
// from panic to warn. It exists for ONE purpose: surveying the whole suite in a
// single pass when introducing a new invariant, instead of fixing violations one
// panic at a time.
//
// It is not a silencer. `make verify` and CI never set it, and nothing in the
// repo does — if you find it set anywhere other than an interactive shell, that
// is a finding.
const EnvMode = "JOBTREE_INVARIANT"

func init() {
	if !UnderTest() {
		// Production installs its own reporter. Until it does, do not panic in a
		// scheduler: an oracle that crashes the control plane is worse than the
		// bug it found.
		Report = func(string, []Violation) {}
		return
	}
	if os.Getenv(EnvMode) == "warn" {
		Report = Warn
	}
}

// Warn is the survey reporter: it prints every violation and keeps going.
//
// It deliberately does NOT de-duplicate. A survey that collapses two tests'
// identical violations into one line hides which tests are affected — and a
// reporter that stays silent about work it did is the exact failure this whole
// effort exists to remove. Noise is the correct trade here.
func Warn(site string, violations []Violation) {
	mu.Lock()
	defer mu.Unlock()
	for _, v := range violations {
		Seen = append(Seen, v)
		fmt.Fprintf(os.Stderr, "INVARIANT-WARN %s | %s\n", site, v)
	}
}

var (
	mu sync.Mutex
	// Seen accumulates every violation observed under Warn.
	Seen []Violation
)

// Panic is the test-mode reporter. It prints a banner before panicking because
// controller-runtime recovers panics inside a Reconcile by default, which would
// otherwise turn a loud, precise violation into an anonymous requeue loop and,
// eventually, an inscrutable test timeout.
func Panic(site string, violations []Violation) {
	if len(violations) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("================ INVARIANT VIOLATION ================\n")
	fmt.Fprintf(&b, "site: %s\n", site)
	for _, v := range violations {
		fmt.Fprintf(&b, "  %s\n", v)
	}
	b.WriteString("\nThis state is illegal. If a test asserts it, the TEST is wrong:\n")
	b.WriteString("see docs/project/adversarial-review-playbook.md, class 8.\n")
	b.WriteString("====================================================\n")
	fmt.Fprint(os.Stderr, b.String())
	panic(b.String())
}

// Check runs both tiers and reports any violation through Report. before may be
// the zero World when no snapshot was taken, in which case only the steady tier
// runs.
func Check(site string, before, after World) {
	violations := CheckSteady(after)
	if len(before.Leases) > 0 || len(before.Runs) > 0 {
		violations = append(violations, CheckTransition(before, after)...)
	}
	if len(violations) > 0 {
		Report(site, violations)
	}
}

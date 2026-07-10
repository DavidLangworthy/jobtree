package controllers

import (
	"fmt"
	"time"

	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// The self-healing half of R27.
//
// pkg/invariant is an oracle: under `go test` it panics, and in production it
// logs and counts. Counting is not healing. An immortal lease that reaches a real
// cluster charges its budget and holds its GPUs forever, and the operator's only
// remedy is a hand-written patch against a Lease nobody can name.
//
// SettleLeases closes those leases by DERIVATION rather than by remembering. It
// does not ask "did some code path forget to close this?" — that is the question
// nobody can answer. It asks "is this lease's run gone or dead?", which is a
// property of the state in front of it. A rule that reads only the present state
// cannot be defeated by a caller that forgot something.
//
// # Why it runs where it runs
//
// Bridge.WithWorld, after the engine and before apply, under the bridge mutex.
// After the engine, because the engine is allowed to pass THROUGH illegal states
// — HandleNodeFailure marks a run Failed inside its lease loop and sweeps after
// it. Before apply, because a lease this closes must reach the API in the same
// pass, and a pod this drops must be deleted in the same pass. Under the mutex,
// because a verdict taken outside it is a verdict about a world that has moved
// (see the #36 refix).
//
// It is NOT hooked into the engine's entry points. The engine's own callers close
// their own leases; a sweep there would race the code it is meant to backstop and
// hide it.
//
// # A sweep is a bug report
//
// Every closure here means some path shirked a duty it owned. In a test binary
// that is a failure, loudly, with the lease named — see Bridge. In production it
// is a Warning event and jobtree_swept_leases_total{rule}, and the cluster keeps
// running. Alert on any nonzero value.
//
// # The rule that is not here
//
// "An open Spare lease with no open Active lease of its group is an orphan" was
// designed and is REFUTED: a leftover spare-only run is an explicitly named legal
// state (see the `allocated == 0` branch of Reconcile), and the plugin mints per
// pod, so a spare legitimately exists before its actives do. Sweeping on it would
// close healthy spares in a normal admission. An invariant that is wrong is not a
// weaker net; it is a reaper. Ship rule 1 only.

// Sweep rule names. Stable strings: they are metric label values.
const (
	// SweepTerminalRun: the run is Failed or Completed and still holds an open
	// lease. Nothing reconciles a corpse, so nothing will ever close it. This is
	// the immortal-lease class, and it is a bug in whichever path made the run
	// terminal without calling releaseRun.
	SweepTerminalRun = "terminal-run"

	// SweepOrphanRun: no Run object exists for the lease at all. The Run was
	// deleted and cleanupDeletedRun has not run, or did not finish.
	//
	// REPORT-ONLY (R12 step 1). Unlike terminal-run, this rule does NOT close the
	// lease or drop the pod — it only records the lease in Sweep.Observed. Its
	// premise is the ABSENCE of a Run, and an absence a single incomplete world
	// load can fake (spec-brief A4), so acting on it risks reaping a live job. Nor
	// is it a test failure: a Run delete and a pod reconcile race by construction.
	// R12's Run finalizer holds the Run in the API until its leases close, making
	// this state unreachable — at which point this rule and its reporting are
	// deleted (R12 verification item #5).
	SweepOrphanRun = "orphan-run"
)

// SweptLease records one closure the sweep had to make, named precisely enough to
// find the path that should have made it.
type SweptLease struct {
	Namespace string
	Name      string
	RunKey    string
	Rule      string
}

func (s SweptLease) String() string {
	return fmt.Sprintf("%s (run %s, rule %s)", s.Name, s.RunKey, s.Rule)
}

func closureReasonFor(rule string) string {
	if rule == SweepOrphanRun {
		return "SweptOrphanRun"
	}
	return "SweptTerminalRun"
}

// Sweep is what one pass of SettleLeases found.
type Sweep struct {
	// Leases is every lease the sweep CLOSED — terminal-run only. In order found.
	Leases []SweptLease
	// Pods is how many containers it dropped alongside those closures.
	Pods int
	// Observed is every orphan-run lease the sweep DID NOT touch. Until R12's Run
	// finalizer makes "an open lease whose Run is absent" an unreachable state, the
	// sweep only COUNTS this — it does not close the lease or drop the pod. An
	// absent Run in one world load is not proof the Run was deleted (spec-brief A4:
	// the load may be incomplete), and acting on that guess can destroy a live,
	// funded, multi-day job. Left open, the lease leaks — and the sweep REPORTS it
	// (a Warning-less log line + jobtree_swept_leases_total{rule=orphan-run}) so an
	// operator can alert on it. (pkg/invariant does NOT catch it: its projection
	// iterates state.Runs, and an orphan's Run is absent, so it is never checked;
	// R26's ledger auditor is what will formalize this cross-plane check.) An
	// omission a human can see, not a destructive action nobody chose.
	Observed []SweptLease
}

// Shirked reports the leases the sweep closed because a path that owned them
// forgot — i.e. every terminal-run closure. Orphan-run is report-only (Observed)
// and never appears here; it races a Run deletion by construction and accuses
// nobody.
func (s Sweep) Shirked() []SweptLease {
	var out []SweptLease
	for _, l := range s.Leases {
		if l.Rule == SweepTerminalRun {
			out = append(out, l)
		}
	}
	return out
}

// Empty reports whether the sweep found nothing at all — nothing closed, nothing
// dropped, nothing to report. The only state a correct engine leaves behind.
func (s Sweep) Empty() bool {
	return len(s.Leases) == 0 && s.Pods == 0 && len(s.Observed) == 0
}

// SettleLeases retires every TERMINAL run in both planes — closes its open leases
// and drops its pods — and merely REPORTS every orphan-run lease without touching
// it.
//
// Terminal acts, orphan only observes, and the asymmetry is the whole point.
// A terminal run rests on POSITIVE evidence: a Run object whose own phase says
// Failed or Completed. Nothing reconciles a corpse, so its open lease is a real
// immortal lease and closing it is safe. An orphan rests on the ABSENCE of a Run,
// which a single incomplete load can fake — so acting on it could reap a live job.
// Report-only restores the pre-R27c property that a wrong world causes an omission
// (a leaked lease the sweep reports and R26 will audit) rather than an action (work
// destroyed). R12's finalizer makes the orphan state unreachable and this can go.
//
// For terminal runs, both planes, always: closing a lease while its container keeps
// running hands the GPU back to the ledger with something still sitting on it, and
// the engine then plans new work onto a GPU the kube-scheduler can never bind — the
// same lie told backwards. Bridge.apply deletes exactly the pods absent from
// State.Pods, so dropping them here is what deletes them.
func SettleLeases(state *ClusterState, now time.Time) Sweep {
	terminal := map[string]bool{} // run key -> terminal (act: close + drop)
	for key, run := range state.Runs {
		if run != nil && (run.Status.Phase == RunPhaseFailed || run.Status.Phase == RunPhaseComplete) {
			terminal[key] = true
		}
	}

	var sweep Sweep
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.Status.Closed {
			continue
		}
		// An empty run name keys to "namespace/", which matches no real Run. The
		// sole committer always names the run; a lease that does not is malformed,
		// not orphaned — leave it entirely alone, and do not even report it as an
		// orphan (a key the sweep cannot trust is not evidence of anything).
		if lease.Spec.RunRef.Name == "" {
			continue
		}
		key := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		switch {
		case terminal[key]:
			// The ledger records WHY. A terminal-run closure is a bug report: some
			// path made the run terminal without calling releaseRun.
			CloseLease(lease, closureReasonFor(SweepTerminalRun), now)
			sweep.Leases = append(sweep.Leases, SweptLease{
				Namespace: lease.Namespace, Name: lease.Name, RunKey: key, Rule: SweepTerminalRun,
			})
		default:
			if run, known := state.Runs[key]; !known || run == nil {
				// Orphan: report only. Do NOT close it, do NOT drop its pod.
				sweep.Observed = append(sweep.Observed, SweptLease{
					Namespace: lease.Namespace, Name: lease.Name, RunKey: key, Rule: SweepOrphanRun,
				})
			}
		}
	}

	// Pods: drop only those of a TERMINAL run. An orphan run's pods are left in
	// place exactly as its lease is — the sweep does not destroy on an absence.
	kept := state.Pods[:0]
	for _, pod := range state.Pods {
		key := keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])
		if pod.Labels[binder.LabelRunName] != "" && terminal[key] {
			sweep.Pods++
			continue
		}
		kept = append(kept, pod)
	}
	state.Pods = kept
	return sweep
}

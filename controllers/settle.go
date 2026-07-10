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
// # The rules that are not here
//
// (1) "An open Spare lease with no open Active lease of its group is an orphan" was
// designed and is REFUTED: a leftover spare-only run is an explicitly named legal
// state (see the `allocated == 0` branch of Reconcile), and the plugin mints per
// pod, so a spare legitimately exists before its actives do. An invariant that is
// wrong is not a weaker net; it is a reaper.
//
// (2) "An open lease whose Run is absent is an orphan — close it" shipped in R27c,
// was demoted to report-only in R12 step 1, and is now DELETED (R12 step 3). Its
// premise — a Run gone while its lease is open — is unreachable: the Run finalizer
// (FundingClosureFinalizer) holds the Run in the API until its leases are closed
// with reason RunDeleted, so a load can no longer show a lease whose Run has
// vanished. A rule that cannot fire is a rule nobody maintains, and it would be the
// one that fires; a genuinely deleted run's leases are closed by cleanupDeletedRun,
// which the Lease→Run watch drives on positive evidence, not by guessing from an
// absence. Ship the terminal-run rule only.

// Sweep rule names. Stable strings: they are metric label values.
const (
	// SweepTerminalRun: the run is Failed or Completed and still holds an open
	// lease. Nothing reconciles a corpse, so nothing will ever close it. This is
	// the immortal-lease class, and it is a bug in whichever path made the run
	// terminal without calling releaseRun.
	SweepTerminalRun = "terminal-run"
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

// Sweep is what one pass of SettleLeases found. Every entry is a shirked duty —
// there is only the terminal-run rule now, and a terminal run holding an open lease
// is always a bug in the path that made it terminal.
type Sweep struct {
	// Leases is every lease the sweep CLOSED, in the order it found them.
	Leases []SweptLease
	// Pods is how many containers it dropped alongside those closures.
	Pods int
}

// Shirked reports the leases the sweep closed because a path that owned them forgot.
// With only the terminal-run rule left, that is every closure.
func (s Sweep) Shirked() []SweptLease { return s.Leases }

// Empty reports whether the sweep found nothing to do — the only state a correct
// engine leaves behind.
func (s Sweep) Empty() bool { return len(s.Leases) == 0 && s.Pods == 0 }

// SettleLeases retires every TERMINAL run in both planes: it closes the run's open
// leases and drops its pods.
//
// It acts only on POSITIVE evidence — a Run object whose own phase says Failed or
// Completed. Nothing reconciles a corpse, so its open lease is a real immortal lease
// and closing it is safe. It deliberately does NOT act on an absent Run: that state
// is unreachable now that the Run finalizer holds a deleting Run until its leases
// close (R12), and a genuine orphan is closed by cleanupDeletedRun on positive
// evidence, not guessed at from a silent load (spec-brief A4).
//
// Both planes, always: closing a lease while its container keeps running hands the
// GPU back to the ledger with something still sitting on it, and the engine then
// plans new work onto a GPU the kube-scheduler can never bind — the same lie told
// backwards. Bridge.apply deletes exactly the pods absent from State.Pods, so
// dropping them here is what deletes them.
func SettleLeases(state *ClusterState, now time.Time) Sweep {
	terminal := map[string]bool{}
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
		key := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		if !terminal[key] {
			continue
		}
		// The ledger records WHY. A terminal-run closure is a bug report: some path
		// made the run terminal without calling releaseRun.
		CloseLease(lease, "SweptTerminalRun", now)
		sweep.Leases = append(sweep.Leases, SweptLease{
			Namespace: lease.Namespace, Name: lease.Name, RunKey: key, Rule: SweepTerminalRun,
		})
	}

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

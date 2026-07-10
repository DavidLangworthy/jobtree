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
	// Unlike the rule above, this one is NOT a test failure. A Run delete and a
	// reconcile of one of its pods race by construction: the bridge can load a
	// world in which the Run is already gone from the API and its cleanup has not
	// yet been observed. Closing the lease there is correct and is not evidence
	// that anybody forgot anything.
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

// Sweep is what one pass of SettleLeases had to repair.
type Sweep struct {
	// Leases is every lease the sweep closed, in the order it found them.
	Leases []SweptLease
	// Pods is how many containers it dropped alongside them.
	Pods int
}

// Shirked reports whether the sweep closed a lease that some engine path owned and
// forgot. Only SweepTerminalRun qualifies: SweepOrphanRun races a Run deletion by
// construction and accuses nobody.
func (s Sweep) Shirked() []SweptLease {
	var out []SweptLease
	for _, l := range s.Leases {
		if l.Rule == SweepTerminalRun {
			out = append(out, l)
		}
	}
	return out
}

// Empty reports whether the sweep found nothing to do, which is the only state a
// correct engine ever leaves behind.
func (s Sweep) Empty() bool { return len(s.Leases) == 0 && s.Pods == 0 }

// SettleLeases retires every run that is terminal or gone, in BOTH planes: it
// closes the run's open leases and drops the run's pods.
//
// Both, always. Closing a lease while its container keeps running hands the GPU
// back to the ledger and leaves something sitting on it — the engine then plans
// new work onto that GPU and the kube-scheduler can never bind it. That is not a
// milder failure than the immortal lease; it is the same lie told backwards, and a
// sweep that told it would be a reaper. Bridge.apply deletes exactly the pods
// absent from State.Pods, so dropping them here is what deletes them.
func SettleLeases(state *ClusterState, now time.Time) Sweep {
	doomed := map[string]string{} // run key -> rule

	for key, run := range state.Runs {
		if run != nil && (run.Status.Phase == RunPhaseFailed || run.Status.Phase == RunPhaseComplete) {
			doomed[key] = SweepTerminalRun
		}
	}
	// A lease or a pod whose Run object does not exist at all. Both planes are
	// consulted, because a Run can be deleted after its pods bound and before its
	// leases were minted, or the other way round.
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.Status.Closed {
			continue
		}
		key := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		if run, known := state.Runs[key]; !known || run == nil {
			doomed[key] = SweepOrphanRun
		}
	}
	for _, pod := range state.Pods {
		key := keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])
		if run, known := state.Runs[key]; !known || run == nil {
			doomed[key] = SweepOrphanRun
		}
	}
	if len(doomed) == 0 {
		return Sweep{}
	}

	var sweep Sweep
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.Status.Closed {
			continue
		}
		key := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		rule, gone := doomed[key]
		if !gone {
			continue
		}
		// The ledger records WHY, not merely that. A month from now the difference
		// between a lease swept off a corpse and one swept off a deleted run is the
		// difference between a bug report and a race.
		CloseLease(lease, closureReasonFor(rule), now)
		sweep.Leases = append(sweep.Leases, SweptLease{
			Namespace: lease.Namespace, Name: lease.Name, RunKey: key, Rule: rule,
		})
	}

	kept := state.Pods[:0]
	for _, pod := range state.Pods {
		key := keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])
		if _, gone := doomed[key]; gone {
			sweep.Pods++
			continue
		}
		kept = append(kept, pod)
	}
	state.Pods = kept
	return sweep
}

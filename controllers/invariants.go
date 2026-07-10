package controllers

import (
	"encoding/json"
	"fmt"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/invariant"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// This file installs the oracle. See pkg/invariant for why it exists and, just
// as importantly, for the four plausible invariants it deliberately does NOT
// enforce because each is false in a state this engine legally produces.
//
// The projection below computes RunnableGPUs and MinRunnableGPUs with the SAME
// helpers the controller uses to make its decisions. That is deliberate: a
// checker with its own private notion of "width" is a second implementation of
// the rule, and two implementations drift. The oracle's power comes from being
// applied to states nobody wrote a test for, not from reasoning independently.
//
// Width is runnableGPUsForRun, NOT baseGangGPUsForRun. The two answer different
// questions, and using the base gang here made the oracle a reaper: the resolver
// may legally cut a malleable run's base group while its grow ranks still cover
// the declared minimum, and the invariant would have panicked on that healthy run.

// snapshotWorld projects the current ClusterState into the invariant package's
// view. O(leases + runs + pods) and allocation-light; it runs on every engine
// entry point.
func (c *RunController) snapshotWorld() invariant.World {
	w := invariant.World{
		Runs:   make([]invariant.Run, 0, len(c.State.Runs)),
		Leases: make([]invariant.Lease, 0, len(c.State.Leases)),
	}

	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		w.Leases = append(w.Leases, invariant.Lease{
			Name:            lease.Name,
			RunKey:          keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name),
			Closed:          lease.Status.Closed,
			GroupIndex:      leaseGroupIndex(lease),
			HasEnded:        lease.Status.Ended != nil && !lease.Status.Ended.IsZero(),
			EndedUnixNano:   endedUnixNano(lease),
			ClosureReason:   lease.Status.ClosureReason,
			SpecFingerprint: specFingerprint(lease),
		})
	}

	// An active-role pod with no lease behind it is width the plugin has not
	// committed yet. The controller emits the pod; the plugin is the sole
	// committer and mints at PreBind. Between those two moments a healthy run
	// legally reports a width it does not yet hold — the swap window.
	activePods := map[string]int{}
	for _, pod := range c.State.Pods {
		if pod.Labels[binder.LabelRunRole] == binder.RoleSpare {
			continue
		}
		runName := pod.Labels[binder.LabelRunName]
		if runName == "" {
			continue
		}
		activePods[keys.NamespacedKey(pod.Namespace, runName)]++
	}
	openActiveLeases := map[string]int{}
	anyLease := map[string]bool{}
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		leaseRun := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		anyLease[leaseRun] = true
		if lease.Status.Closed || lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		openActiveLeases[leaseRun]++
	}
	// Every pod of every role, including the spares — a terminal run must leave
	// none of them behind, and a spare's container holds a GPU exactly as an
	// active's does.
	podsOfRun := map[string]int{}
	for _, pod := range c.State.Pods {
		// A pod under graceful deletion has left the workload plane: the
		// kubelet is draining it, its GPUs are being reclaimed, and the engine
		// will never plan onto it. Counting it as a pod the run "still holds"
		// turned the routine window after every ordinary completion — terminal
		// run, lease already closed, pod still Terminating for its grace
		// period — into a false INV-TERMINAL-NO-PODS panic on the next
		// reconcile of ANY run (adversarial review 2026-07-10, c74e0ef:
		// reproduced against a live envtest apiserver).
		if pod.Terminating {
			continue
		}
		if runName := pod.Labels[binder.LabelRunName]; runName != "" {
			podsOfRun[keys.NamespacedKey(pod.Namespace, runName)]++
		}
	}

	for key, run := range c.State.Runs {
		if run == nil {
			continue
		}
		w.Runs = append(w.Runs, invariant.Run{
			Key:             key,
			Phase:           run.Status.Phase,
			Terminal:        run.Status.Phase == RunPhaseFailed || run.Status.Phase == RunPhaseComplete,
			RunnableGPUs:    runnableGPUsForRun(key, c.State.Leases),
			MinRunnableGPUs: minRunnableGPUs(run),
			Pods:            podsOfRun[key],
			AwaitingMint:    activePods[key] > openActiveLeases[key],
			KnownToLedger:   anyLease[key] || podsOfRun[key] > 0,
		})
	}
	return w
}

func endedUnixNano(lease *v1.Lease) int64 {
	if lease.Status.Ended == nil || lease.Status.Ended.IsZero() {
		return 0
	}
	return lease.Status.Ended.Time.UnixNano()
}

// specFingerprint is a deterministic encoding of a Lease's immutable half. Go's
// encoding/json emits struct fields in declaration order, so equal specs
// fingerprint equally. A marshalling error cannot make two different specs look
// the same: it makes every spec look distinct, which fails closed.
func specFingerprint(lease *v1.Lease) string {
	raw, err := json.Marshal(lease.Spec)
	if err != nil {
		return fmt.Sprintf("unmarshalable:%s:%v", lease.Name, err)
	}
	return string(raw)
}

// checkInvariants asserts the engine's postconditions. Call it deferred, from
// every engine entry point, so it runs on EVERY return — including the error
// returns, because Bridge.WithWorld applies the state diff even when the engine
// returns an error.
//
// `before` may be the zero World; the transition tier is then skipped.
func (c *RunController) checkInvariants(site string, before invariant.World) {
	invariant.Check(site, before, c.snapshotWorld())
}

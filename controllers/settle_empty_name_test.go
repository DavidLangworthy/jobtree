package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// The sweep-safety lens flagged this: a lease or pod carrying an EMPTY run-name
// label keys to "namespace/", which matches no real Run. The sole committer always
// names the run, so such an object is malformed — and a sweep that destroys work
// must never act on a key it cannot trust. With the orphan rule now deleted (R12)
// the sweep only acts on terminal runs, and "namespace/" is never terminal, so a
// malformed object is left alone by construction. This guards that it stays so.
func TestSweepLeavesMalformedEmptyRunNameObjectsAlone(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	// An open lease and a pod whose RunRef/label name is empty. No Run exists for
	// them (none can — "namespace/" is not a real key).
	lease := prodLease("orphanish", "", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)
	pod := tpPod("nameless-active-0", "", "node-a")

	state := settleState(now,
		map[string]*v1.Run{},
		[]v1.GPULease{lease},
		[]binder.PodManifest{pod})

	sweep := SettleLeases(state, now)

	if !sweep.Empty() {
		t.Fatalf("the sweep acted on a malformed empty-run-name object: %+v (pods dropped: %d)", sweep.Leases, sweep.Pods)
	}
	if closed, _ := closureOf(state, "orphanish"); closed {
		t.Errorf("an empty-run-name lease was closed as an orphan; it is malformed, not orphaned")
	}
	if len(state.Pods) != 1 {
		t.Errorf("an empty-run-name pod was deleted by the sweep")
	}
}

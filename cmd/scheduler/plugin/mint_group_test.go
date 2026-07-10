package plugin

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// R28b. The sole committer refuses to mint a lease that names no placement group.
//
// A lease without one merges the run's groups into one, and the ledger cannot detect
// the lie: the resolver cuts whole runs instead of groups, the elastic loop shrinks in
// whole-run units, and a reclaim asking for "the pods of this group" gets the pods of
// the entire run. So the gate is here, at the one place a Lease becomes a fact.
func TestMintRefusesAPodWithNoPlacementGroup(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "train-active-0",
		Labels: map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
	}}
	got, err := mintGroupIndex(pod)
	if err == nil {
		t.Fatalf("minted group %q for a pod with no group label; the mint must fail closed", got)
	}
	if !errors.Is(err, ErrNoPlacementGroup) {
		t.Errorf("callers must be able to use errors.Is, not match the message; got %#v", err)
	}
}

func TestMintCarriesThePodsPlacementGroupOntoTheLease(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "train-active-9",
		Labels: map[string]string{
			binder.LabelRunName:    "train",
			binder.LabelRunRole:    binder.RoleActive,
			binder.LabelGroupIndex: "2",
		},
	}}
	got, err := mintGroupIndex(pod)
	if err != nil {
		t.Fatalf("mintGroupIndex: %v", err)
	}
	if got != "2" {
		t.Errorf("group = %q, want %q — the lease must name the group the pod was planned into", got, "2")
	}
}

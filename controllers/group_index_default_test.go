package controllers

import (
	"testing"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// #61 pt1: leaseGroupIndex of an unlabelled lease must read "" — NOT "0". The R28
// reaper was exactly this default: three consumers (the resolver's per-group cut,
// the elastic shrink, the node-failure swap) address work BY group, and a missing
// group silently defaulting to "0" makes all three hit the wrong ranks. Worse, it
// hides from INV-GROUP-STAMPED, which reads leaseGroupIndex and would see a "0" as
// a legitimately stamped group and never fire. Nothing else pins this directly, so
// a mutation from "" to "0" survives every other test — this is its guard.
func TestLeaseGroupIndexMissingIsEmptyNotZero(t *testing.T) {
	unlabelled := &v1.GPULease{ObjectMeta: v1.ObjectMeta{Name: "l"}}
	if got := leaseGroupIndex(unlabelled); got != "" {
		t.Fatalf("leaseGroupIndex(unlabelled) = %q, want \"\" — a missing group must never default to \"0\"", got)
	}
	nilLabels := &v1.GPULease{ObjectMeta: v1.ObjectMeta{Name: "l", Labels: map[string]string{"other": "x"}}}
	if got := leaseGroupIndex(nilLabels); got != "" {
		t.Fatalf("leaseGroupIndex(no group label) = %q, want \"\"", got)
	}
	labelled := &v1.GPULease{ObjectMeta: v1.ObjectMeta{Name: "l", Labels: map[string]string{binder.LabelGroupIndex: "2"}}}
	if got := leaseGroupIndex(labelled); got != "2" {
		t.Fatalf("leaseGroupIndex = %q, want \"2\"", got)
	}
}

package invariant

import (
	"strings"
	"testing"
)

// The oracle is a safety net, and a safety net nobody tests is decorative. Each
// test below asserts BOTH that a real violation is caught and that a legal state
// this engine actually produces is left alone — because an invariant that is
// wrong is not a weaker net, it is a reaper.

func ids(vs []Violation) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return strings.Join(out, ",")
}

func TestTerminalRunHoldingAnOpenLeaseIsAViolation(t *testing.T) {
	w := World{
		Runs:   []Run{{Key: "default/train", Phase: "Failed", Terminal: true, KnownToLedger: true}},
		Leases: []Lease{{Name: "l1", RunKey: "default/train", Closed: false, GroupIndex: "0"}},
	}
	got := CheckSteady(w)
	if len(got) != 1 || got[0].ID != TerminalPresent {
		t.Fatalf("a Failed run holding an open lease charges its budget forever; want %s, got [%s]", TerminalPresent, ids(got))
	}
	if !strings.Contains(got[0].Detail, "l1") {
		t.Errorf("the violation must name the offending lease so it can be found: %q", got[0].Detail)
	}
}

func TestTerminalRunHoldingOnlyClosedLeasesIsLegal(t *testing.T) {
	w := World{
		Runs:   []Run{{Key: "default/train", Phase: "Completed", Terminal: true, KnownToLedger: true}},
		Leases: []Lease{{Name: "l1", RunKey: "default/train", Closed: true, HasEnded: true, ClosureReason: "Completed"}},
	}
	if got := CheckSteady(w); len(got) != 0 {
		t.Fatalf("a settled run is legal, got [%s]", ids(got))
	}
}

// A closed lease with no Ended timestamp is not "mostly closed". pkg/funding's
// effectiveEnd bills it to its START instant, so it accrues nothing for its whole
// life: a silent under-charge, invisible in every dashboard.
func TestHalfClosedLeaseIsAViolation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lease Lease
	}{
		{"no ended timestamp", Lease{Name: "l1", Closed: true, ClosureReason: "Completed"}},
		{"no closure reason", Lease{Name: "l1", Closed: true, HasEnded: true}},
		{"neither", Lease{Name: "l1", Closed: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckSteady(World{Leases: []Lease{tc.lease}})
			if len(got) != 1 || got[0].ID != CloseStamped {
				t.Fatalf("want %s, got [%s]", CloseStamped, ids(got))
			}
		})
	}
}

func TestRunningRunBelowItsMinimumWidthIsAViolation(t *testing.T) {
	w := World{Runs: []Run{{
		Key: "default/train", Phase: "Running",
		RunnableGPUs: 2, MinRunnableGPUs: 4, KnownToLedger: true,
	}}}
	got := CheckSteady(w)
	if len(got) != 1 || got[0].ID != WidthAssembled {
		t.Fatalf("a gang missing ranks reports healthy while charging a budget; want %s, got [%s]", WidthAssembled, ids(got))
	}
}

// The swap window. HandleNodeFailure closes the failed active AND the spare,
// emits a swap POD, and sets Running; the plugin mints the replacement lease
// later, at PreBind. A width invariant that fired here would reap a healthy,
// recovering run — which is why AwaitingMint exists.
func TestRunningRunAwaitingAMintIsLegalEvenAtZeroWidth(t *testing.T) {
	w := World{Runs: []Run{{
		Key: "default/train", Phase: "Running",
		RunnableGPUs: 0, MinRunnableGPUs: 4, AwaitingMint: true, KnownToLedger: true,
	}}}
	if got := CheckSteady(w); len(got) != 0 {
		t.Fatalf("a run whose replacement pod has not been minted yet is legal, got [%s]", ids(got))
	}
}

// A run the ledger has never heard of cannot have a width checked against it.
// Many unit tests build such a run to exercise follow/completion logic.
func TestRunningRunUnknownToTheLedgerIsNotWidthChecked(t *testing.T) {
	w := World{Runs: []Run{{
		Key: "default/upstream", Phase: "Running",
		RunnableGPUs: 0, MinRunnableGPUs: 4, KnownToLedger: false,
	}}}
	if got := CheckSteady(w); len(got) != 0 {
		t.Fatalf("a run with no lease and no pod has not been placed, got [%s]", ids(got))
	}
}

// ...but a run that LOST every lease it held still has closed ones, so it stays
// checked. That is the case worth catching.
func TestRunningRunThatLostItsWholeGangIsStillChecked(t *testing.T) {
	w := World{
		Runs:   []Run{{Key: "default/train", Phase: "Running", RunnableGPUs: 0, MinRunnableGPUs: 4, KnownToLedger: true}},
		Leases: []Lease{{Name: "l1", RunKey: "default/train", Closed: true, HasEnded: true, ClosureReason: "NodeFailure"}},
	}
	got := CheckSteady(w)
	if len(got) != 1 || got[0].ID != WidthAssembled {
		t.Fatalf("a run reporting Running over a dead gang must be caught; got [%s]", ids(got))
	}
}

func TestPendingRunHoldingOpenLeasesIsLegal(t *testing.T) {
	// The half-assembled gang: "start together or not at all" means the run parks
	// Pending while it holds the leases it has and tops up the rest.
	w := World{
		Runs:   []Run{{Key: "default/train", Phase: "Pending", RunnableGPUs: 2, MinRunnableGPUs: 4, KnownToLedger: true}},
		Leases: []Lease{{Name: "l1", RunKey: "default/train", GroupIndex: "0"}},
	}
	if got := CheckSteady(w); len(got) != 0 {
		t.Fatalf("a run assembling its gang is legal, got [%s]", ids(got))
	}
}

func TestTransitionCatchesTheWaysALeaseCanStopBeingAFact(t *testing.T) {
	base := Lease{Name: "l1", Closed: true, HasEnded: true, EndedUnixNano: 100, ClosureReason: "Completed", SpecFingerprint: "A"}
	for _, tc := range []struct {
		name  string
		after Lease
	}{
		{"reopened", Lease{Name: "l1", Closed: false, GroupIndex: "0", SpecFingerprint: "A"}},
		{"spec mutated", Lease{Name: "l1", Closed: true, HasEnded: true, EndedUnixNano: 100, ClosureReason: "Completed", SpecFingerprint: "B"}},
		{"ending moved", Lease{Name: "l1", Closed: true, HasEnded: true, EndedUnixNano: 200, ClosureReason: "Completed", SpecFingerprint: "A"}},
		{"reason rewritten", Lease{Name: "l1", Closed: true, HasEnded: true, EndedUnixNano: 100, ClosureReason: "Shrink", SpecFingerprint: "A"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckTransition(World{Leases: []Lease{base}}, World{Leases: []Lease{tc.after}})
			if len(got) != 1 || got[0].ID != ClosedMonotone {
				t.Fatalf("want %s, got [%s]", ClosedMonotone, ids(got))
			}
		})
	}
}

// Bridge.load only snapshots leases that existed when the world was loaded, and
// bridge.apply legitimately creates a lease — and may even close it — within a
// single pass. A new mint has no "before" to be monotone against.
func TestTransitionExemptsNewMints(t *testing.T) {
	before := World{Leases: []Lease{{Name: "old", Closed: false, GroupIndex: "0", SpecFingerprint: "A"}}}
	after := World{Leases: []Lease{
		{Name: "old", Closed: true, HasEnded: true, EndedUnixNano: 1, ClosureReason: "Swap", SpecFingerprint: "A"},
		{Name: "fresh", Closed: true, HasEnded: true, EndedUnixNano: 2, ClosureReason: "Completed", SpecFingerprint: "Z"},
	}}
	if got := CheckTransition(before, after); len(got) != 0 {
		t.Fatalf("closing an existing lease and minting a new one in one pass is legal, got [%s]", ids(got))
	}
}

// An open lease with no placement group makes the resolver, the elastic loop and the
// node-failure swap each address the wrong ranks, and none of them can tell. Before
// R28b the sole committer stamped none and three consumers defaulted it to "0".
func TestOpenLeaseWithNoPlacementGroupIsAViolation(t *testing.T) {
	w := World{Leases: []Lease{{Name: "l1", RunKey: "default/train", Closed: false}}}
	got := CheckSteady(w)
	if len(got) != 1 || got[0].ID != GroupStamped {
		t.Fatalf("want %s, got [%s]", GroupStamped, ids(got))
	}
}

// A CLOSED lease is a settled fact; it is not addressed by anything, and old ledgers
// legitimately hold pre-R28b closures. Only open leases are constrained.
func TestClosedLeaseNeedsNoPlacementGroup(t *testing.T) {
	w := World{Leases: []Lease{{Name: "l1", RunKey: "default/train", Closed: true, HasEnded: true, ClosureReason: "Completed"}}}
	if got := CheckSteady(w); len(got) != 0 {
		t.Fatalf("a settled lease is legal whatever it was labelled, got [%s]", ids(got))
	}
}

// The reason this package needs no opt-in: a test author cannot forget to enable
// the oracle, because being a test binary IS the enablement.
func TestUnderTestIsTrueInsideAGoTestBinary(t *testing.T) {
	if !UnderTest() {
		t.Fatal("UnderTest() must be true here, or the oracle silently never runs in CI")
	}
}

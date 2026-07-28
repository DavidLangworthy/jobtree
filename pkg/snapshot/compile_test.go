package snapshot

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

var base = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func tp(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }

func budget(ns, owner string, envs ...v1.BudgetEnvelope) v1.Budget {
	return v1.Budget{
		ObjectMeta: metav1.ObjectMeta{Name: owner + "-budget", Namespace: ns},
		Spec:       v1.BudgetSpec{Owner: owner, Envelopes: envs},
	}
}

func envelope(name string, concurrency int32, start, end time.Time) v1.BudgetEnvelope {
	return v1.BudgetEnvelope{
		Name:        name,
		Flavor:      "H100",
		Selector:    map[string]string{"region": "us-west"},
		Concurrency: concurrency,
		Start:       tp(start),
		End:         tp(end),
	}
}

func grant(ns, name, granteeOwner, granteeNS string, cap int32, start, end time.Time) v1.Grant {
	return v1.Grant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, ResourceVersion: "100"},
		Spec: v1.GrantSpec{
			GranteeOwner:     granteeOwner,
			GranteeNamespace: granteeNS,
			Caps:             []v1.GrantCap{{Flavor: "H100", MaxConcurrency: cap}},
			Start:            tp(start),
			End:              tp(end),
		},
	}
}

func uids(pairs ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

func principalOf(t *testing.T, res Result, owner string) v1.SnapshotPrincipal {
	t.Helper()
	for _, p := range res.Snapshot.Spec.Principals {
		if p.Owner == owner {
			return p
		}
	}
	t.Fatalf("principal %q absent from the snapshot", owner)
	return v1.SnapshotPrincipal{}
}

// THE PRODUCER-AUTHORIZATION SPECIMEN (DESIGN-v5 §11).
//
// The producer is the whole trust boundary, because no invariant a published
// document can express says "this changeset was authored by someone entitled to
// make it" — a forged edge and a legitimate one compile to the same shape. So
// the check has to live where writes are seen.
//
// Here an outsider namespace, bound to no principal at all, writes a Grant that
// would hand itself authority over a real principal. Every field in it is
// well-formed. It must not compile in, and the reason must be WHERE it was
// written, not what it names.
func TestGrantAuthoredOutsideItsSubtreeDoesNotCompileIn(t *testing.T) {
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour))),
			budget("ns-team", "org:team", envelope("west", 32, base, base.Add(720*time.Hour))),
		},
		Grants: []v1.Grant{
			// The attack: "ns-outsider" holds no Budget, so it is bound to no
			// principal. It writes a perfectly-shaped Grant handing org:team a
			// large allocation it has no authority to give.
			grant("ns-outsider", "forged", "org:team", "ns-team", 1000, base, base.Add(720*time.Hour)),
		},
		NamespaceUIDs: uids("ns-lead", "uid-lead", "ns-team", "uid-team", "ns-outsider", "uid-outsider"),
		Now:           base,
	}

	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	reason, refused := res.Rejected["ns-outsider/forged"]
	if !refused {
		t.Fatalf("a Grant from a namespace bound to no principal compiled in; rejected=%v quarantined=%v",
			res.Rejected, res.Quarantined)
	}
	if !contains(reason, "bound to no principal") {
		t.Errorf("refusal should say the AUTHOR has no authority, got %q", reason)
	}

	// And the victim is untouched: it keeps its own 32, not the forged 1000, and
	// gains no inbound authority.
	team := principalOf(t, res, "org:team")
	if team.InboundGrant != nil {
		t.Errorf("forged grant became inbound authority: %+v", team.InboundGrant)
	}
	if len(team.Envelopes) != 1 || team.Envelopes[0].Concurrency != 32 {
		t.Errorf("victim's own allocation changed: %+v", team.Envelopes)
	}
}

// INV-NO-SELF-GRANT: the grantor comes from the namespace's UID, never a field,
// so granting to yourself is caught structurally rather than by trusting a name.
func TestSelfGrantIsQuarantined(t *testing.T) {
	in := Input{
		Budgets: []v1.Budget{budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour)))},
		Grants:  []v1.Grant{grant("ns-lead", "self", "org:lead", "ns-lead", 1000, base, base.Add(720*time.Hour))},

		NamespaceUIDs: uids("ns-lead", "uid-lead"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, bad := res.Quarantined["ns-lead/self"]; !bad {
		t.Fatalf("a self-grant compiled in: %+v", res)
	}
	if c := principalOf(t, res, "org:lead").Envelopes[0].Concurrency; c != 64 {
		t.Errorf("self-grant changed the grantor's own allocation to %d", c)
	}
}

// §2c: a Grant naming a principal that does not exist is REJECTED, not
// quarantined, precisely so it cannot spring alive later when something with
// that name eventually appears.
func TestGrantToNonexistentPrincipalIsRejectedNotQuarantined(t *testing.T) {
	in := Input{
		Budgets:       []v1.Budget{budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour)))},
		Grants:        []v1.Grant{grant("ns-lead", "ghost", "org:ghost", "ns-ghost", 8, base, base.Add(720*time.Hour))},
		NamespaceUIDs: uids("ns-lead", "uid-lead"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, q := res.Quarantined["ns-lead/ghost"]; q {
		t.Error("an unresolvable endpoint was quarantined; it must be REJECTED so it cannot spring alive later")
	}
	if _, r := res.Rejected["ns-lead/ghost"]; !r {
		t.Fatalf("unresolvable grant was neither rejected nor quarantined: %+v", res)
	}
}

// §2a: EVERYTHING clamps, no dimension rejects. Both the concurrency and the
// TIME axis clamp, and authored overhang is legal, visible over-allocation.
func TestEverythingClampsAndOverhangIsVisible(t *testing.T) {
	envEnd := base.Add(720 * time.Hour)
	grantEnd := base.Add(240 * time.Hour) // authority ends well before the envelope does
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-lead", "org:lead", envelope("west", 64, base, envEnd)),
			budget("ns-team", "org:team", envelope("west", 40, base, envEnd)),
		},
		Grants:        []v1.Grant{grant("ns-lead", "to-team", "org:team", "ns-team", 10, base, grantEnd)},
		NamespaceUIDs: uids("ns-lead", "uid-lead", "ns-team", "uid-team"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(res.Quarantined) != 0 {
		t.Fatalf("over-allocation must CLAMP, never quarantine: %+v", res.Quarantined)
	}
	e := principalOf(t, res, "org:team").Envelopes[0]

	if e.Concurrency != 10 {
		t.Errorf("effectiveConcurrency = %d, want min(40, 10) = 10", e.Concurrency)
	}
	if e.OverAllocatedBy == nil || *e.OverAllocatedBy != 30 {
		t.Errorf("overAllocatedBy = %v, want 30 authored over authority", e.OverAllocatedBy)
	}
	if !e.End.Time.Equal(grantEnd) {
		t.Errorf("effectiveWindow end = %v, want the grant's %v (envelope ∩ grant)", e.End.Time, grantEnd)
	}
	// The temporal diagnostic: a number cannot say "your envelope extends past
	// its authority", and that is a different repair from lowering one.
	if e.OverAllocatedUntil == nil || !e.OverAllocatedUntil.Time.Equal(envEnd) {
		t.Errorf("overAllocatedUntil = %v, want the authored end %v", e.OverAllocatedUntil, envEnd)
	}
}

// §2a: an over-allocation that does NOT exist must be ABSENT, not zero (Ruling
// 3 / §9) — a zero is indistinguishable from "conservation is not built yet".
func TestNoOverAllocationIsAbsentNotZero(t *testing.T) {
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour))),
			budget("ns-team", "org:team", envelope("west", 8, base, base.Add(720*time.Hour))),
		},
		Grants:        []v1.Grant{grant("ns-lead", "to-team", "org:team", "ns-team", 32, base, base.Add(720*time.Hour))},
		NamespaceUIDs: uids("ns-lead", "uid-lead", "ns-team", "uid-team"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	e := principalOf(t, res, "org:team").Envelopes[0]
	if e.OverAllocatedBy != nil {
		t.Errorf("overAllocatedBy must be ABSENT when there is none, got %v", *e.OverAllocatedBy)
	}
	if e.OverAllocatedUntil != nil {
		t.Errorf("overAllocatedUntil must be absent when the window fits, got %v", e.OverAllocatedUntil)
	}
}

// §2b: INV-SINGLE-INBOUND-AUTHORITY is TIME-INDEXED. Non-overlapping grants are
// a STAGED HANDOFF and must both survive — counting objects globally instead
// would quarantine the replacement while the incumbent lives and drop the
// grantee to zero at expiry. Seamless reparenting has to work.
func TestStagedHandoffIsNotTwoComposingAuthorities(t *testing.T) {
	mid := base.Add(240 * time.Hour)
	end := base.Add(480 * time.Hour)
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-a", "org:a", envelope("west", 64, base, end)),
			budget("ns-b", "org:b", envelope("west", 64, base, end)),
			budget("ns-p", "org:p", envelope("west", 16, base, end)),
		},
		Grants: []v1.Grant{
			grant("ns-a", "first", "org:p", "ns-p", 8, base, mid),
			grant("ns-b", "second", "org:p", "ns-p", 8, mid, end), // starts exactly where the first ends
		},
		NamespaceUIDs: uids("ns-a", "uid-a", "ns-b", "uid-b", "ns-p", "uid-p"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(res.Quarantined) != 0 {
		t.Fatalf("a staged handoff must not quarantine anything: %+v", res.Quarantined)
	}
}

// The other half of the same rule: genuinely OVERLAPPING inbound authority is
// ambiguous and the later write is the guilty one. Quarantine is guilt-scoped —
// the incumbent wins (§4.2).
func TestOverlappingInboundAuthorityQuarantinesTheNewcomer(t *testing.T) {
	end := base.Add(480 * time.Hour)
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-a", "org:a", envelope("west", 64, base, end)),
			budget("ns-b", "org:b", envelope("west", 64, base, end)),
			budget("ns-p", "org:p", envelope("west", 16, base, end)),
		},
		Grants: []v1.Grant{
			grant("ns-a", "incumbent", "org:p", "ns-p", 8, base, end),
			grant("ns-b", "newcomer", "org:p", "ns-p", 8, base.Add(time.Hour), end),
		},
		NamespaceUIDs: uids("ns-a", "uid-a", "ns-b", "uid-b", "ns-p", "uid-p"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, bad := res.Quarantined["ns-b/newcomer"]; !bad {
		t.Fatalf("the overlapping newcomer should be quarantined: %+v", res.Quarantined)
	}
	if _, bad := res.Quarantined["ns-a/incumbent"]; bad {
		t.Error("THE INCUMBENT WINS: quarantine attaches to the causative new write, not to both")
	}
	if g := principalOf(t, res, "org:p").InboundGrant; g == nil || g.Name != "incumbent" {
		t.Errorf("the incumbent's authority must survive, got %+v", g)
	}
}

// §2b: absent inbound authority means ZERO — never a fallback to the grantee's
// own Budget, and never an implicit root.
func TestNoInboundAuthorityMeansZeroNotFallback(t *testing.T) {
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour))),
			budget("ns-team", "org:team", envelope("west", 32, base, base.Add(720*time.Hour))),
		},
		NamespaceUIDs: uids("ns-lead", "uid-lead", "ns-team", "uid-team"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	team := principalOf(t, res, "org:team")
	if team.InboundGrant != nil {
		t.Errorf("no grant exists, so there must be no inbound authority: %+v", team.InboundGrant)
	}
	// It is a recorded ROOT rather than an implicit one: roots are stated in the
	// document, so a reader never has to infer authority from absence.
	var isRoot bool
	for _, r := range res.Snapshot.Spec.Roots {
		if r == "org:team" {
			isRoot = true
		}
	}
	if !isRoot {
		t.Error("a principal with no inbound authority must be RECORDED in roots, not left implicit")
	}
}

// INV-BINDING-INJECTIVE: one namespace binds one principal, and one principal
// binds one namespace. A compiler that cannot say who a principal IS cannot
// meaningfully quarantine writes against them, so this is a hard error.
func TestBindingInjectivityIsAHardError(t *testing.T) {
	t.Run("two owners in one namespace", func(t *testing.T) {
		in := Input{
			Budgets: []v1.Budget{
				budget("ns-shared", "org:a", envelope("west", 8, base, base.Add(720*time.Hour))),
				budget("ns-shared", "org:b", envelope("east", 8, base, base.Add(720*time.Hour))),
			},
			NamespaceUIDs: uids("ns-shared", "uid-shared"),
			Now:           base,
		}
		if _, err := Compile(in); err == nil {
			t.Fatal("two principals in one namespace must not compile")
		}
	})
	t.Run("one owner in two namespaces", func(t *testing.T) {
		in := Input{
			Budgets: []v1.Budget{
				budget("ns-1", "org:a", envelope("west", 8, base, base.Add(720*time.Hour))),
				budget("ns-2", "org:a", envelope("east", 8, base, base.Add(720*time.Hour))),
			},
			NamespaceUIDs: uids("ns-1", "uid-1", "ns-2", "uid-2"),
			Now:           base,
		}
		if _, err := Compile(in); err == nil {
			t.Fatal("one principal in two namespaces must not compile")
		}
	})
}

// Identity keys on the namespace UID, not its name. A namespace deleted and
// recreated under the same name is a DIFFERENT principal, and a Grant written
// against the old one must not follow the name to the new one.
func TestGranteeNamespaceMustMatchByUIDNotName(t *testing.T) {
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour))),
			budget("ns-team", "org:team", envelope("west", 32, base, base.Add(720*time.Hour))),
		},
		// The grant names ns-team, but org:team is actually bound to a namespace
		// whose UID differs — the name was reused.
		Grants:        []v1.Grant{grant("ns-lead", "stale", "org:team", "ns-team-old", 8, base, base.Add(720*time.Hour))},
		NamespaceUIDs: uids("ns-lead", "uid-lead", "ns-team", "uid-team", "ns-team-old", "uid-team-old"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, r := res.Rejected["ns-lead/stale"]; !r {
		t.Fatalf("a grant naming a reused namespace name must not resolve: %+v", res)
	}
}

// INV-SNAP-MONOTONE, and the reason contentHash exists: a recompile that
// changes nothing must NOT burn a version, so "the published version changed"
// always means "something actually changed".
func TestVersionIsMonotoneAndStableUnderNoOpRecompile(t *testing.T) {
	in := Input{
		Budgets:       []v1.Budget{budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour)))},
		NamespaceUIDs: uids("ns-lead", "uid-lead"),
		Now:           base,
	}
	first, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if first.Snapshot.Spec.SnapshotVersion != "1" {
		t.Fatalf("first version = %q, want 1", first.Snapshot.Spec.SnapshotVersion)
	}

	// Recompile with no change, at a LATER instant: EffectiveFrom moves, content
	// does not, so the version must hold.
	in.Prior = &first.Snapshot
	in.Now = base.Add(time.Hour)
	second, err := Compile(in)
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if second.Snapshot.Spec.SnapshotVersion != "1" {
		t.Errorf("a no-op recompile burned a version: %q", second.Snapshot.Spec.SnapshotVersion)
	}

	// A real change must advance it.
	in.Prior = &second.Snapshot
	in.Budgets = []v1.Budget{budget("ns-lead", "org:lead", envelope("west", 65, base, base.Add(720*time.Hour)))}
	third, err := Compile(in)
	if err != nil {
		t.Fatalf("third compile: %v", err)
	}
	if third.Snapshot.Spec.SnapshotVersion != "2" {
		t.Errorf("a real change must advance the version, got %q", third.Snapshot.Spec.SnapshotVersion)
	}
}

// §2a: grant expiry clamps authority to ZERO; it never quarantines. Ordinary
// expiry must never become quarantine ambiguity — that is what regenerates the
// fuse this whole design exists to remove.
func TestExpiredGrantClampsToZeroAndNeverQuarantines(t *testing.T) {
	in := Input{
		Budgets: []v1.Budget{
			budget("ns-lead", "org:lead", envelope("west", 64, base, base.Add(720*time.Hour))),
			budget("ns-team", "org:team", envelope("west", 32, base, base.Add(720*time.Hour))),
		},
		// Authority ended before the envelope even opens.
		Grants:        []v1.Grant{grant("ns-lead", "expired", "org:team", "ns-team", 8, base.Add(-48*time.Hour), base.Add(-24*time.Hour))},
		NamespaceUIDs: uids("ns-lead", "uid-lead", "ns-team", "uid-team"),
		Now:           base,
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(res.Quarantined) != 0 {
		t.Fatalf("expiry must never quarantine: %+v", res.Quarantined)
	}
	if c := principalOf(t, res, "org:team").Envelopes[0].Concurrency; c != 0 {
		t.Errorf("an expired grant must clamp authority to zero, got %d", c)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

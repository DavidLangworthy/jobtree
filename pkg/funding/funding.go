// Package funding implements the derived funding classification from
// docs/project/quota-semantics.md (Decision 3): funded vs. opportunistic is
// a pure, deterministic function of (budgets, leases, clock), recomputed by
// whoever needs it. Nothing here is ever stored on a CRD; leases record
// immutable consumption facts and this package evaluates those facts against
// current quota. The ranked greedy fill matches specs/QuotaEvaluation.tla:
// a claim's class depends only on claims ranked above it, so adding or
// removing a claim never reshuffles the survivors.
package funding

import (
	"sort"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// Class is a derived funding class. It is an evaluation artifact, never a
// field on any CRD (quota-semantics.md Decision 3).
type Class string

const (
	// ClassOwned backs a claim with the requester's own envelope.
	ClassOwned Class = "Owned"
	// ClassShared backs a claim with a family envelope's excess.
	ClassShared Class = "Shared"
	// ClassBorrowed backs a claim with a sponsor's envelope via its lending
	// policy. Borrowed capacity is contractual: it is charged ahead of the
	// family fill (the lender pre-consented and declared caps), so the
	// lender's later claims do not re-rank it opportunistic.
	ClassBorrowed Class = "Borrowed"
	// ClassUnfunded backs a claim with nothing. Unfunded work keeps running
	// and is reclaimed first, by lottery, when funded work needs capacity.
	ClassUnfunded Class = "Unfunded"
)

// Proximity tiers for the ranking, from the envelope owner's perspective.
// Sponsors sit outside the tier ranking: existing sponsor leases are
// contract carve-outs (see ClassBorrowed), prospective sponsor claims are
// junior to everything.
const (
	TierOwner   = 1
	TierChild   = 2
	TierSibling = 3
	TierCousin  = 4
	tierNone    = 0 // no family relationship
)

// FamilyGraph tracks owner parent/child relationships for proximity tiers.
// Budgets declare parents; the graph is shared by the evaluation (who keeps
// capacity under pressure) and the cover planner (where to look for
// capacity), which quota-semantics.md requires to agree.
type FamilyGraph struct {
	parents  map[string]map[string]struct{}
	children map[string]map[string]struct{}
}

// NewFamilyGraph builds the owner DAG from budget parent declarations.
func NewFamilyGraph(budgets []v1.Budget) *FamilyGraph {
	g := &FamilyGraph{
		parents:  make(map[string]map[string]struct{}),
		children: make(map[string]map[string]struct{}),
	}
	for i := range budgets {
		owner := budgets[i].Spec.Owner
		g.addOwner(owner)
		for _, parent := range budgets[i].Spec.Parents {
			g.AddEdge(parent, owner)
		}
	}
	return g
}

func (g *FamilyGraph) addOwner(owner string) {
	if _, ok := g.parents[owner]; !ok {
		g.parents[owner] = make(map[string]struct{})
	}
	if _, ok := g.children[owner]; !ok {
		g.children[owner] = make(map[string]struct{})
	}
}

// AddEdge records parent→child.
func (g *FamilyGraph) AddEdge(parent, child string) {
	g.addOwner(parent)
	g.addOwner(child)
	g.children[parent][child] = struct{}{}
	g.parents[child][parent] = struct{}{}
}

// Parents returns the sorted parents of owner.
func (g *FamilyGraph) Parents(owner string) []string {
	return sortedKeys(g.parents[owner])
}

// Siblings returns the sorted set of owners sharing a parent with owner.
func (g *FamilyGraph) Siblings(owner string) []string {
	set := make(map[string]struct{})
	for parent := range g.parents[owner] {
		for child := range g.children[parent] {
			if child != owner {
				set[child] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}

// Cousins returns the sorted children of the owner's parents' siblings.
func (g *FamilyGraph) Cousins(owner string) []string {
	set := make(map[string]struct{})
	for parent := range g.parents[owner] {
		for grand := range g.parents[parent] {
			for aunt := range g.children[grand] {
				if aunt == parent {
					continue
				}
				for cousin := range g.children[aunt] {
					set[cousin] = struct{}{}
				}
			}
		}
	}
	return sortedKeys(set)
}

// Tier returns the proximity tier of runOwner on envelopeOwner's envelopes:
// TierOwner for the owner's own runs, then children, siblings, cousins.
// tierNone (0) means no family relationship — such claims are sponsor
// territory, classified by the lending contract instead of the tier walk.
func (g *FamilyGraph) Tier(envelopeOwner, runOwner string) int {
	if envelopeOwner == runOwner {
		return TierOwner
	}
	if _, ok := g.children[envelopeOwner][runOwner]; ok {
		return TierChild
	}
	for parent := range g.parents[runOwner] {
		if _, ok := g.children[parent][envelopeOwner]; ok && envelopeOwner != runOwner {
			return TierSibling
		}
	}
	for _, cousin := range g.Cousins(runOwner) {
		if cousin == envelopeOwner {
			return TierCousin
		}
	}
	return tierNone
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// classForTier maps a family tier to the class a funded claim gets.
func classForTier(tier int) Class {
	if tier == TierOwner {
		return ClassOwned
	}
	return ClassShared
}

// EnvelopeKey identifies an envelope across budgets (envelope names repeat
// between budgets of the same owner). Namespace is part of the identity: Budgets
// are namespaced, so two tenants can each own a Budget of the same name in their
// own namespace — without it, their envelopes collide in the funding index and one
// tenant silently charges (or shadows) the other's budget (Codex #1 / task #62).
type EnvelopeKey struct {
	Namespace string
	Budget    string
	Envelope  string
}

// claimKey identifies a claim: one run's demand on one envelope.
type claimKey struct {
	env    EnvelopeKey
	runKey string
}

// claim is the unit of ranking: the set of a run's open leases paid by one
// envelope. Fixed-width runs are classified all-or-nothing; malleable runs
// fill lease by lease ("the greedy fill funds as much width as quota
// affords"), lowest group index first so partial funding demotes the same
// groups the shrink path would cut.
type claim struct {
	key       claimKey
	tier      int  // family tier, or tierNone for sponsor/stranger claims
	sponsored bool // true when tierNone: classified by the lending contract
	admitted  time.Time
	name      string // deterministic tiebreak (run key; lease name for orphans)
	malleable bool
	leases    []*leaseFact
	width     int32
}

// leaseFact is one open or closed lease with its parsed placement width.
type leaseFact struct {
	lease      *v1.GPULease
	width      int32
	groupIndex int
	name       string
}

// rankLess orders claims by (tier, admission time, name) — the normative
// ranking from quota-semantics.md Decision 3. Sponsor claims are ordered
// among themselves by (admission time, name); the caller keeps the two
// pools separate.
func rankLess(a, b *claim) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if !a.admitted.Equal(b.admitted) {
		return a.admitted.Before(b.admitted)
	}
	return a.name < b.name
}

// LeaseKey names a lease for classification lookups.
func LeaseKey(lease *v1.GPULease) string {
	return keys.NamespacedKey(lease.Namespace, lease.Name)
}

// envelopeSharable reports whether family members (tiers 2-4) may draw on
// this envelope's excess. `sharing: none` opts an envelope out of family
// sharing entirely; it does not affect the owner or the lending policy.
func envelopeSharable(env *v1.BudgetEnvelope) bool {
	return env.Sharing != v1.SharingNone
}

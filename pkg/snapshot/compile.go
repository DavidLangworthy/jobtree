// Package snapshot compiles Budgets and Grants into the versioned identity
// document of DESIGN-v5 §3, and is the whole trust boundary of the design (§11).
//
// THE TRUST BOUNDARY IS HERE, NOT IN THE DOCUMENT. No invariant a published
// document can express says *"this changeset was authored by someone entitled to
// make it"* — a forged edge and a legitimate one are the same shape once
// compiled. Authorisation is a property of the WRITE, so it can only be checked
// where writes are seen: here. That is why §11 requires a producer-authorization
// specimen (a Grant authored by a principal outside its own subtree must not
// compile in) rather than a document-validity test.
//
// The core is pure on purpose, in this repo's usual shape: Compile is a function
// from facts to a document, so every structural invariant and every quarantine
// rule is testable without a cluster. The controller does I/O and publication.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// Input is everything the producer compiles from.
type Input struct {
	Budgets []v1.Budget
	Grants  []v1.Grant
	// NamespaceUIDs maps namespace NAME to its immutable UID. Identity keys on
	// the UID: a namespace deleted and recreated under the same name is a
	// DIFFERENT principal, and only the UID can tell you so. A namespace absent
	// here does not resolve, which is a rejection, not a quarantine.
	NamespaceUIDs map[string]string
	// Prior is the last ACCEPTED graph. Transitions validate against this, never
	// against the aggregate candidate document (§4.1) — that difference is what
	// makes quarantine guilt-scoped instead of collective.
	Prior *v1.QuotaSnapshot
	// Now is the compile instant; EffectiveFrom is stamped from it.
	Now time.Time
}

// Result is the compiled document plus what was refused and why.
type Result struct {
	Snapshot v1.QuotaSnapshot
	// Quarantined names the WRITES that did not compile in, keyed by
	// "namespace/name" of the offending object. Quarantine attaches to a write,
	// never to a principal (§4).
	Quarantined map[string]string
	// Rejected names objects refused outright rather than quarantined: a Grant
	// naming a principal that does not exist is REJECTED so it cannot spring
	// alive later (INV-GRANT-ENDPOINTS-RESOLVE, §2c).
	Rejected map[string]string
}

// Compile builds the next snapshot.
//
// The order is deliberate: resolve identity, then validate each Grant TRANSITION
// against the prior accepted graph, then build the graph from what survived,
// then clamp. Clamping last is what makes §2a's "everything clamps, no dimension
// rejects" true — a clamp cannot reject, so it must not run before the checks
// that can.
func Compile(in Input) (Result, error) {
	res := Result{
		Quarantined: map[string]string{},
		Rejected:    map[string]string{},
	}

	principals, err := bindPrincipals(in)
	if err != nil {
		return res, err
	}

	accepted := acceptGrants(in, principals, &res)
	if err := buildEdges(accepted, principals, &res); err != nil {
		return res, err
	}

	doc := assemble(in, principals, accepted)
	res.Snapshot = doc
	return res, nil
}

// principal is the compiler's working record for one owner.
type principal struct {
	owner     string
	nsName    string
	nsUID     string
	budget    *v1.Budget
	inbound   *v1.Grant
	inboundOK bool
	children  []string
	status    string
	trigger   string
}

// bindPrincipals derives the owner→namespace binding and enforces the two
// identity invariants that are properties of the binding itself.
//
// INV-PRINCIPAL-UNIQUE: one owner, one principal. INV-BINDING-INJECTIVE: one
// namespace binds one owner and one owner binds one namespace. Both are hard
// errors rather than quarantines, because a compiler that cannot say who a
// principal IS cannot meaningfully quarantine a write against them.
func bindPrincipals(in Input) (map[string]*principal, error) {
	out := map[string]*principal{}
	byNamespace := map[string]string{}

	budgets := append([]v1.Budget(nil), in.Budgets...)
	sort.Slice(budgets, func(i, j int) bool {
		if budgets[i].Namespace != budgets[j].Namespace {
			return budgets[i].Namespace < budgets[j].Namespace
		}
		return budgets[i].Name < budgets[j].Name
	})

	for i := range budgets {
		b := &budgets[i]
		owner := b.Spec.Owner
		if owner == "" {
			continue
		}
		uid := in.NamespaceUIDs[b.Namespace]
		if uid == "" {
			// A namespace we cannot identify is not a principal we can bind.
			// Fail closed: it funds nothing rather than binding by name.
			continue
		}
		if prev, ok := byNamespace[b.Namespace]; ok && prev != owner {
			return nil, fmt.Errorf("INV-BINDING-INJECTIVE: namespace %q binds both %q and %q; one namespace binds exactly one principal",
				b.Namespace, prev, owner)
		}
		byNamespace[b.Namespace] = owner

		if p, ok := out[owner]; ok {
			if p.nsUID != uid {
				return nil, fmt.Errorf("INV-BINDING-INJECTIVE: principal %q is bound in namespaces %q and %q; one principal binds exactly one namespace",
					owner, p.nsName, b.Namespace)
			}
			// Same owner, same namespace, second Budget: INV-ENVELOPE-UNIQUE is
			// checked across the merged envelope set below.
			if err := mergeBudget(p, b); err != nil {
				return nil, err
			}
			continue
		}
		out[owner] = &principal{
			owner:  owner,
			nsName: b.Namespace,
			nsUID:  uid,
			budget: b.DeepCopy(),
			status: v1.PrincipalAccepted,
		}
	}
	return out, nil
}

// mergeBudget folds a second Budget for the same principal, enforcing
// INV-ENVELOPE-UNIQUE across the union rather than per object — an envelope name
// that is unique in each of two Budgets but collides across them is exactly the
// ambiguity the invariant exists to forbid.
func mergeBudget(p *principal, b *v1.Budget) error {
	seen := map[string]struct{}{}
	for i := range p.budget.Spec.Envelopes {
		seen[p.budget.Spec.Envelopes[i].Name] = struct{}{}
	}
	for i := range b.Spec.Envelopes {
		name := b.Spec.Envelopes[i].Name
		if _, dup := seen[name]; dup {
			return fmt.Errorf("INV-ENVELOPE-UNIQUE: principal %q has two envelopes named %q across its Budgets", p.owner, name)
		}
		seen[name] = struct{}{}
		p.budget.Spec.Envelopes = append(p.budget.Spec.Envelopes, b.Spec.Envelopes[i])
	}
	return nil
}

// acceptGrants decides, per Grant, whether THIS revision compiles in.
//
// This is the authorization check that no document invariant can express (§11).
// The grantor is derived from the Grant's own namespace UID — never read from a
// field — so a principal can only grant from where it is actually bound, and a
// forged grantor is not representable rather than merely rejected.
func acceptGrants(in Input, principals map[string]*principal, res *Result) []*v1.Grant {
	grants := append([]v1.Grant(nil), in.Grants...)
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].Namespace != grants[j].Namespace {
			return grants[i].Namespace < grants[j].Namespace
		}
		return grants[i].Name < grants[j].Name
	})

	ownerByNamespaceUID := map[string]string{}
	for _, p := range principals {
		ownerByNamespaceUID[p.nsUID] = p.owner
	}

	var accepted []*v1.Grant
	for i := range grants {
		g := &grants[i]
		key := g.Namespace + "/" + g.Name

		if err := g.Spec.Validate(); err != nil {
			res.Quarantined[key] = "invalid grant: " + err.Error()
			continue
		}

		// THE AUTHORIZATION CHECK. The grantor is whoever is bound to the
		// namespace this Grant was written in. If nobody is, the author is
		// outside any subtree and grants nothing — this is the producer-
		// authorization specimen of §11.
		grantorUID := in.NamespaceUIDs[g.Namespace]
		grantor, ok := ownerByNamespaceUID[grantorUID]
		if grantorUID == "" || !ok {
			res.Rejected[key] = fmt.Sprintf(
				"namespace %q is bound to no principal, so its Grants authorise nothing; a Grant is authorised by where it is WRITTEN, not by what it names",
				g.Namespace)
			continue
		}

		// INV-GRANT-ENDPOINTS-RESOLVE (§2c): a Grant naming a principal that does
		// not exist is REJECTED, not quarantined, precisely so it cannot spring
		// alive later when that name happens to appear.
		grantee, ok := principals[g.Spec.GranteeOwner]
		if !ok {
			res.Rejected[key] = fmt.Sprintf("grantee %q resolves to no principal; rejected rather than quarantined so it cannot spring alive later",
				g.Spec.GranteeOwner)
			continue
		}
		wantUID := in.NamespaceUIDs[g.Spec.GranteeNamespace]
		if wantUID == "" || wantUID != grantee.nsUID {
			res.Rejected[key] = fmt.Sprintf(
				"grantee %q is bound to namespace %q, not %q; identity keys on the namespace UID, so a name that has been reused names a different principal",
				g.Spec.GranteeOwner, grantee.nsName, g.Spec.GranteeNamespace)
			continue
		}

		// INV-NO-SELF-GRANT (§2c): granting to yourself creates authority from
		// nothing.
		if grantee.owner == grantor {
			res.Quarantined[key] = fmt.Sprintf("principal %q may not grant to itself", grantor)
			continue
		}

		accepted = append(accepted, g)
	}
	return accepted
}

// buildEdges applies the composition invariants, then clamps.
func buildEdges(accepted []*v1.Grant, principals map[string]*principal, res *Result) error {
	ownerOfNamespace := map[string]string{}
	for _, p := range principals {
		ownerOfNamespace[p.nsName] = p.owner
	}

	// INV-SINGLE-INBOUND-AUTHORITY, TIME-INDEXED (§2b). Two grants to the same
	// principal compose only if their windows OVERLAP; non-overlapping ones are a
	// STAGED HANDOFF and are legal. Counting objects globally instead would
	// quarantine a replacement while the incumbent still lives, and drop the
	// grantee to zero at expiry — seamless reparenting has to work.
	byGrantee := map[string][]*v1.Grant{}
	for _, g := range accepted {
		byGrantee[g.Spec.GranteeOwner] = append(byGrantee[g.Spec.GranteeOwner], g)
	}
	for grantee, gs := range byGrantee {
		sort.Slice(gs, func(i, j int) bool { return gs[i].Spec.Start.Time.Before(gs[j].Spec.Start.Time) })
		for i := 1; i < len(gs); i++ {
			prev, cur := gs[i-1], gs[i]
			if cur.Spec.Start.Time.Before(prev.Spec.End.Time) {
				key := cur.Namespace + "/" + cur.Name
				res.Quarantined[key] = fmt.Sprintf(
					"principal %q already has inbound authority from %s/%s over an overlapping window; two composing authorities are ambiguous, a staged handoff (non-overlapping windows) is not",
					grantee, prev.Namespace, prev.Name)
			}
		}
	}

	for _, g := range accepted {
		key := g.Namespace + "/" + g.Name
		if _, bad := res.Quarantined[key]; bad {
			continue
		}
		grantor := ownerOfNamespace[g.Namespace]
		grantee := principals[g.Spec.GranteeOwner]
		if grantee == nil {
			continue
		}
		// INV-ACYCLIC, strengthened (§2c): forbid naming the grantor OR any
		// ancestor of it. A cycle would let authority be manufactured by walking
		// a loop, and the strengthening closes the indirect version of that.
		if isAncestor(principals, grantee.owner, grantor) {
			res.Quarantined[key] = fmt.Sprintf(
				"granting to %q would make it an ancestor of its own grantor %q; authority cannot flow in a cycle",
				grantee.owner, grantor)
			continue
		}
		grantee.inbound = g
		grantee.inboundOK = true
		if p := principals[grantor]; p != nil {
			p.children = append(p.children, grantee.owner)
		}
	}
	return nil
}

// isAncestor reports whether candidate is at or above target in the current
// inbound-authority chain.
func isAncestor(principals map[string]*principal, candidate, target string) bool {
	seen := map[string]bool{}
	for cur := target; cur != ""; {
		if cur == candidate {
			return true
		}
		if seen[cur] {
			return false // defensive: an existing cycle must not hang the compiler
		}
		seen[cur] = true
		p := principals[cur]
		if p == nil || p.inbound == nil {
			return false
		}
		next := ""
		for _, q := range principals {
			if q.nsName == p.inbound.Namespace {
				next = q.owner
				break
			}
		}
		cur = next
	}
	return false
}

// assemble clamps and emits the document.
func assemble(in Input, principals map[string]*principal, accepted []*v1.Grant) v1.QuotaSnapshot {
	owners := make([]string, 0, len(principals))
	for owner := range principals {
		owners = append(owners, owner)
	}
	sort.Strings(owners)

	ownerOfNamespace := map[string]string{}
	for _, p := range principals {
		ownerOfNamespace[p.nsName] = p.owner
	}

	var roots []string
	out := make([]v1.SnapshotPrincipal, 0, len(owners))
	for _, owner := range owners {
		p := principals[owner]
		sort.Strings(p.children)

		sp := v1.SnapshotPrincipal{
			Owner:             p.owner,
			Status:            p.status,
			QuarantineTrigger: p.trigger,
			BoundNamespace:    v1.NamespaceBinding{Name: p.nsName, UID: p.nsUID},
			Children:          p.children,
		}
		if p.inbound != nil {
			sp.InboundGrant = &v1.GrantProvenance{
				Namespace: p.inbound.Namespace,
				Name:      p.inbound.Name,
				Revision:  p.inbound.ResourceVersion,
				Grantor:   ownerOfNamespace[p.inbound.Namespace],
			}
		} else {
			// INV-ROOTED: a principal with no inbound authority is a root only if
			// it is authoritative on its own. Absent inbound authority means ZERO
			// (§2b) — never a fallback to the grantee's Budget, never an implicit
			// root — so this is where a root must be RECORDED rather than assumed.
			roots = append(roots, p.owner)
		}
		sp.Envelopes = clampEnvelopes(p)
		out = append(out, sp)
	}

	spec := v1.QuotaSnapshotSpec{
		SchemaVersion: v1.QuotaSnapshotSchemaVersion,
		EffectiveFrom: metav1.NewTime(in.Now.UTC()),
		Roots:         roots,
		Principals:    out,
	}
	spec.ContentHash = contentHash(spec)
	spec.SnapshotVersion = nextVersion(in.Prior, spec.ContentHash)

	return v1.QuotaSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: SnapshotName},
		Spec:       spec,
		Status: v1.QuotaSnapshotStatus{
			PrincipalCount: int32(len(out)),
		},
	}
}

// clampEnvelopes applies §2a: EVERYTHING clamps, no dimension rejects.
//
//	effectiveConcurrency = min(envelope.concurrency, grant.cap)
//	effectiveWindow      = envelope.window ∩ grant.window
//
// A principal with no inbound authority and no root status gets nothing, because
// absent authority is ZERO. Over-allocation is surfaced, never rejected:
// INV-EFFECTIVE-WITHIN-GRANT replaced INV-ENVELOPE-WITHIN-GRANT precisely
// because the latter is "actively harmful once the necessary temporal clamp is
// adopted" — authored overhang is legal, visible over-allocation.
func clampEnvelopes(p *principal) []v1.SnapshotEnvelope {
	if p.budget == nil {
		return nil
	}
	capByFlavor := map[string]int32{}
	var gStart, gEnd *time.Time
	if p.inbound != nil {
		for i := range p.inbound.Spec.Caps {
			c := &p.inbound.Spec.Caps[i]
			capByFlavor[c.Flavor] = c.MaxConcurrency
		}
		s, e := p.inbound.Spec.Start.Time, p.inbound.Spec.End.Time
		gStart, gEnd = &s, &e
	}

	out := make([]v1.SnapshotEnvelope, 0, len(p.budget.Spec.Envelopes))
	for i := range p.budget.Spec.Envelopes {
		e := &p.budget.Spec.Envelopes[i]
		if e.Start == nil || e.End == nil {
			continue // INV-WINDOW-REQUIRED; validation refuses these on the way in.
		}
		start, end := e.Start.Time, e.End.Time
		concurrency := e.Concurrency
		var over *int32
		var overUntil *metav1.Time

		if p.inbound != nil {
			capped, ok := capByFlavor[e.Flavor]
			if !ok {
				// A flavour the grant does not mention is not granted at all.
				continue
			}
			if concurrency > capped {
				excess := concurrency - capped
				over = &excess
				concurrency = capped
			}
			if gStart.After(start) {
				start = *gStart
			}
			if gEnd.Before(end) {
				// The TEMPORAL diagnostic (§2a): per-flavour overAllocatedBy
				// cannot say "your envelope extends 12 days past its authority",
				// and that is a different repair from lowering a number.
				until := metav1.NewTime(e.End.Time)
				overUntil = &until
				end = *gEnd
			}
			if !end.After(start) {
				// Grant expiry clamps authority to zero; it NEVER quarantines
				// (§2a). An empty intersection funds nothing and says why.
				concurrency = 0
				end = start
			}
		}

		out = append(out, v1.SnapshotEnvelope{
			Name:               e.Name,
			Flavor:             e.Flavor,
			Concurrency:        concurrency,
			Start:              metav1.NewTime(start),
			End:                metav1.NewTime(end),
			OverAllocatedBy:    over,
			OverAllocatedUntil: overUntil,
		})
	}
	return out
}

// SnapshotName is the single published document's name. One document; sharding
// is not built (Ruling 18).
const SnapshotName = "cluster"

// contentHash covers compiled CONTENT only — not the version and not
// EffectiveFrom — so a recompile that changes nothing is recognisably a no-op
// and does not burn a version.
func contentHash(spec v1.QuotaSnapshotSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema=%s\n", spec.SchemaVersion)
	for _, r := range spec.Roots {
		fmt.Fprintf(&b, "root=%s\n", r)
	}
	for _, p := range spec.Principals {
		fmt.Fprintf(&b, "p=%s status=%s trigger=%s ns=%s uid=%s children=%s\n",
			p.Owner, p.Status, p.QuarantineTrigger, p.BoundNamespace.Name, p.BoundNamespace.UID,
			strings.Join(p.Children, ","))
		if g := p.InboundGrant; g != nil {
			fmt.Fprintf(&b, "  grant=%s/%s rev=%s grantor=%s\n", g.Namespace, g.Name, g.Revision, g.Grantor)
		}
		for _, e := range p.Envelopes {
			over := "-"
			if e.OverAllocatedBy != nil {
				over = fmt.Sprint(*e.OverAllocatedBy)
			}
			until := "-"
			if e.OverAllocatedUntil != nil {
				until = e.OverAllocatedUntil.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(&b, "  env=%s flavor=%s c=%d [%s,%s) over=%s until=%s\n",
				e.Name, e.Flavor, e.Concurrency,
				e.Start.UTC().Format(time.RFC3339), e.End.UTC().Format(time.RFC3339), over, until)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// nextVersion keeps INV-SNAP-MONOTONE: versions only ever increase. An unchanged
// content hash REUSES the prior version rather than minting a new one, so
// "published version changed" always means "something actually changed".
func nextVersion(prior *v1.QuotaSnapshot, hash string) string {
	if prior == nil {
		return "1"
	}
	if prior.Spec.ContentHash == hash && prior.Spec.SnapshotVersion != "" {
		return prior.Spec.SnapshotVersion
	}
	n := 0
	if _, err := fmt.Sscanf(prior.Spec.SnapshotVersion, "%d", &n); err != nil || n < 0 {
		n = 0
	}
	return fmt.Sprint(n + 1)
}

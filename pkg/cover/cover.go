// Package cover plans who pays for a run's GPUs. Since R14/R15 it is the
// admission-side view of the one funding derivation (pkg/funding): the
// planner walks envelopes in the proximity order of quota-semantics.md
// Decision 2 — own first, then parents, siblings, cousins (family excess
// needs no lending policy), then sponsors under their lending contracts —
// same-location before cross-location at each tier, and asks the
// evaluation how much width the prospective claim could get funded. The
// phase order and the ranking function agree by construction: both walk
// funding.FamilyGraph tiers.
package cover

import (
	"errors"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/funding"
)

// Request describes a funding request for a run.
type Request struct {
	Owner    string
	Flavor   string
	Quantity int32
	Location map[string]string
	Now      time.Time
	// Admitted ranks the prospective claim: a run keeps the rank of its
	// original admission when it grows. Zero means "now".
	Admitted time.Time
	// RunKey is the prospective run's namespaced key; it drives the
	// deterministic name tiebreak among same-second, same-tier claims so
	// admission agrees with the classifier's ranking. Empty is allowed
	// (conservative estimate).
	RunKey        string
	AllowBorrow   bool
	Sponsors      []string
	MaxBorrowGPUs *int32
}

// Segment is a single envelope assignment.
type Segment struct {
	Namespace    string
	BudgetName   string
	EnvelopeName string
	Owner        string
	Quantity     int32
	Borrowed     bool
}

// Plan captures the funding plan across envelopes.
type Plan struct {
	Segments []Segment
}

// FailureReason classifies plan failures.
type FailureReason string

const (
	FailureReasonInvalidRequest       FailureReason = "InvalidRequest"
	FailureReasonNoMatchingEnvelope   FailureReason = "NoEnvelope"
	FailureReasonInsufficientCapacity FailureReason = "InsufficientCapacity"
	FailureReasonACLRejected          FailureReason = "ACLDenied"
	FailureReasonBorrowLimit          FailureReason = "BorrowLimit"
)

// PlanError describes why planning failed.
type PlanError struct {
	Reason FailureReason
	Msg    string
}

func (p *PlanError) Error() string {
	return p.Msg
}

// Inventory plans admissions against one funding evaluation.
type Inventory struct {
	eval *funding.Evaluation
	// envelopesByOwner lists each owner's envelopes in deterministic
	// (budget, envelope) order.
	envelopesByOwner map[string][]*funding.EnvelopeAccount
}

// NewInventory wraps the evaluation for admission planning.
func NewInventory(eval *funding.Evaluation) *Inventory {
	byOwner := make(map[string][]*funding.EnvelopeAccount)
	for _, acct := range eval.Envelopes() {
		byOwner[acct.Owner] = append(byOwner[acct.Owner], acct)
	}
	return &Inventory{eval: eval, envelopesByOwner: byOwner}
}

type phase struct {
	owners       []string
	requireSame  bool
	requireOther bool
	sponsor      bool
}

// buildPhases emits the proximity-major walk. Within each family tier the
// same-location pass precedes the cross-location pass (Decision 2);
// sponsors come last and only when the run opts into borrowing.
func (inv *Inventory) buildPhases(req Request) []phase {
	graph := inv.eval.Graph
	tiers := [][]string{
		{req.Owner},
		graph.Parents(req.Owner),
		graph.Siblings(req.Owner),
		graph.Cousins(req.Owner),
	}
	var phases []phase
	for _, owners := range tiers {
		if len(owners) == 0 {
			continue
		}
		phases = append(phases,
			phase{owners: owners, requireSame: true},
			phase{owners: owners, requireOther: true},
		)
	}
	if req.AllowBorrow && len(req.Sponsors) > 0 {
		phases = append(phases,
			phase{owners: req.Sponsors, requireSame: true, sponsor: true},
			phase{owners: req.Sponsors, requireOther: true, sponsor: true},
		)
	}
	return phases
}

// Plan computes a funding assignment respecting family sharing and lending.
func (inv *Inventory) Plan(req Request) (Plan, error) {
	if req.Quantity <= 0 {
		return Plan{}, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "quantity must be positive"}
	}
	if req.Owner == "" || req.Flavor == "" {
		return Plan{}, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "owner and flavor must be set"}
	}
	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	if req.Admitted.IsZero() {
		req.Admitted = req.Now
	}

	admission := inv.eval.NewAdmission(req.Owner, req.Admitted, req.RunKey)
	remaining := req.Quantity
	borrowedTotal := int32(0)
	borrowAttempted := false
	borrowLimited := false
	var segments []Segment

	for _, ph := range inv.buildPhases(req) {
		if remaining == 0 {
			break
		}
		for _, owner := range unique(ph.owners) {
			if remaining == 0 {
				break
			}
			for _, acct := range inv.envelopesByOwner[owner] {
				if remaining == 0 {
					break
				}
				if acct.Spec.Flavor != req.Flavor {
					continue
				}
				sameLocation := matchesLocation(acct.Spec.Selector, req.Location)
				if ph.requireSame && !sameLocation {
					continue
				}
				if ph.requireOther && sameLocation {
					continue
				}
				if !windowAllowsAdmission(acct.Spec, req.Now) {
					continue
				}
				borrow := ph.sponsor && acct.Owner != req.Owner
				if borrow {
					borrowAttempted = true
					if !req.AllowBorrow {
						return Plan{}, &PlanError{Reason: FailureReasonACLRejected, Msg: "borrowing not allowed"}
					}
				}

				take := admission.Available(acct.Key, borrow)
				if take <= 0 {
					continue
				}
				if borrow && req.MaxBorrowGPUs != nil {
					allowed := *req.MaxBorrowGPUs - borrowedTotal
					if allowed <= 0 {
						borrowLimited = true
						continue
					}
					if take > allowed {
						take = allowed
						borrowLimited = true
					}
				}
				if take > remaining {
					take = remaining
				}
				admission.Take(acct.Key, take)
				segments = append(segments, Segment{
					Namespace:    acct.Key.Namespace,
					BudgetName:   acct.Key.Budget,
					EnvelopeName: acct.Key.Envelope,
					Owner:        acct.Owner,
					Quantity:     take,
					Borrowed:     borrow,
				})
				if borrow {
					borrowedTotal += take
				}
				remaining -= take
			}
		}
	}

	if remaining > 0 {
		reason := FailureReasonInsufficientCapacity
		if len(segments) == 0 {
			reason = FailureReasonNoMatchingEnvelope
		} else if borrowAttempted && borrowLimited && req.MaxBorrowGPUs != nil {
			reason = FailureReasonBorrowLimit
		}
		return Plan{}, &PlanError{Reason: reason, Msg: "insufficient capacity for request"}
	}

	return Plan{Segments: segments}, nil
}

// ValidateInventory ensures the inventory has at least one envelope for the
// owner.
func (inv *Inventory) ValidateInventory(owner string) error {
	if len(inv.envelopesByOwner[owner]) == 0 {
		return errors.New("owner has no budgets")
	}
	return nil
}

func matchesLocation(selector, location map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	for key, sel := range selector {
		if sel == "*" {
			continue
		}
		loc, ok := location[key]
		if !ok {
			return false
		}
		if loc != sel {
			return false
		}
	}
	return true
}

func windowAllowsAdmission(env v1.BudgetEnvelope, now time.Time) bool {
	if env.Start != nil && now.Before(env.Start.Time) {
		if env.PreActivation != nil {
			return env.PreActivation.AllowAdmission
		}
		return false
	}
	if env.End != nil && !now.Before(env.End.Time) {
		return false
	}
	return true
}

func unique(list []string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, item := range list {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

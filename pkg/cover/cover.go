package cover

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/budget"
)

// Request describes a funding request for a run.
type Request struct {
	Owner            string
	Flavor           string
	Quantity         int32
	Location         map[string]string
	Now              time.Time
	ExpectedDuration time.Duration
	AllowBorrow      bool
	Sponsors         []string
}

// Segment is a single envelope assignment.
type Segment struct {
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
)

// PlanError describes why planning failed.
type PlanError struct {
	Reason FailureReason
	Msg    string
}

func (p *PlanError) Error() string {
	return p.Msg
}

// Inventory indexes budgets by owner for planning.
type Inventory struct {
	owners map[string][]*budget.BudgetState
	graph  *familyGraph
}

// NewInventory constructs an inventory from budget states.
func NewInventory(states []*budget.BudgetState) *Inventory {
	owners := make(map[string][]*budget.BudgetState)
	graph := newFamilyGraph()
	for _, st := range states {
		owner := st.Budget.Spec.Owner
		owners[owner] = append(owners[owner], st)
		graph.addOwner(owner)
		for _, parent := range st.Budget.Spec.Parents {
			graph.addEdge(parent, owner)
		}
	}
	return &Inventory{owners: owners, graph: graph}
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

	phases := inv.buildPhases(req)
	remaining := req.Quantity
	var segments []Segment
	alloc := newAllocationTracker(req.ExpectedDuration)

	for _, phase := range phases {
		if remaining == 0 {
			break
		}
		owners := unique(phase.owners)
		for _, owner := range owners {
			if remaining == 0 {
				break
			}
			states := inv.owners[owner]
			for _, st := range states {
				envelopes := sortedEnvelopeStates(st, req.Flavor)
				for _, env := range envelopes {
					if remaining == 0 {
						break
					}
					sameLocation := matchesLocation(env.Spec.Selector, req.Location)
					if phase.requireSame && !sameLocation {
						continue
					}
					if phase.requireOther && sameLocation {
						continue
					}
					if !windowAllowsAdmission(env.Spec, req.Now) {
						continue
					}
					if phase.sponsor && !req.AllowBorrow {
						return Plan{}, &PlanError{Reason: FailureReasonACLRejected, Msg: "borrowing not allowed"}
					}
					if phase.sponsor {
						if env.Spec.Lending == nil || !env.Spec.Lending.Allow {
							continue
						}
						if !allowsBorrower(env.Spec.Lending, req.Owner) {
							continue
						}
					}

					maxAlloc := inv.headroomForEnvelope(env, alloc, phase.sponsor)
					if maxAlloc <= 0 {
						continue
					}
					if maxAlloc > remaining {
						maxAlloc = remaining
					}
					allocated := alloc.allocate(env, maxAlloc, phase.sponsor)
					if allocated == 0 {
						continue
					}
					segments = append(segments, Segment{
						BudgetName:   st.Budget.ObjectMeta.Name,
						EnvelopeName: env.Spec.Name,
						Owner:        env.Owner,
						Quantity:     allocated,
						Borrowed:     phase.sponsor && env.Owner != req.Owner,
					})
					remaining -= allocated
				}
			}
		}
	}

	if remaining > 0 {
		reason := FailureReasonInsufficientCapacity
		if len(segments) == 0 {
			reason = FailureReasonNoMatchingEnvelope
		}
		return Plan{}, &PlanError{Reason: reason, Msg: "insufficient capacity for request"}
	}

	return Plan{Segments: segments}, nil
}

func (inv *Inventory) headroomForEnvelope(env *budget.EnvelopeState, alloc *allocationTracker, sponsor bool) int32 {
	expectedHours := alloc.expectedHoursPerGPU
	additional := budget.Usage{GPUHours: float64(alloc.pendingAllocation(env)) * expectedHours}
	headroom := budget.EnvelopeHeadroom(env, additional)
	if headroom.Concurrency <= 0 {
		return 0
	}
	limit := headroom.Concurrency
	if expectedHours > 0 && headroom.GPUHours != nil {
		limit = min(limit, int32(math.Floor(*headroom.GPUHours/expectedHours)))
	}
	for _, cap := range env.Aggregates {
		aggAdditional := budget.Usage{GPUHours: float64(alloc.pendingAggregate(cap)) * expectedHours}
		aggHeadroom := budget.AggregateHeadroom(cap, aggAdditional)
		if aggHeadroom.Concurrency < limit {
			limit = aggHeadroom.Concurrency
		}
		if limit <= 0 {
			return 0
		}
		if expectedHours > 0 && aggHeadroom.GPUHours != nil {
			limit = min(limit, int32(math.Floor(*aggHeadroom.GPUHours/expectedHours)))
		}
	}
	if sponsor {
		policy := env.Spec.Lending
		if policy != nil {
			if policy.MaxConcurrency != nil {
				available := *policy.MaxConcurrency - (env.Usage.BorrowedConcurrency + alloc.pendingBorrowed(env))
				if available < limit {
					limit = available
				}
			}
			if expectedHours > 0 && policy.MaxGPUHours != nil {
				availableHours := float64(*policy.MaxGPUHours) - (env.Usage.BorrowedGPUHours + alloc.pendingBorrowedHours(env))
				if availableHours < 0 {
					availableHours = 0
				}
				hoursLimit := int32(math.Floor(availableHours / expectedHours))
				if hoursLimit < limit {
					limit = hoursLimit
				}
			}
		}
	}
	if limit < 0 {
		return 0
	}
	return limit
}

type phase struct {
	owners       []string
	requireSame  bool
	requireOther bool
	sponsor      bool
}

func (inv *Inventory) buildPhases(req Request) []phase {
	siblings := inv.graph.siblings(req.Owner)
	parents := inv.graph.parentsOf(req.Owner)
	cousins := inv.graph.cousins(req.Owner)

	phases := []phase{
		{owners: []string{req.Owner}, requireSame: true},
		{owners: siblings, requireSame: true},
		{owners: parents, requireSame: true},
		{owners: []string{req.Owner}, requireOther: true},
		{owners: siblings, requireOther: true},
		{owners: parents, requireOther: true},
		{owners: cousins, requireSame: true},
		{owners: cousins, requireOther: true},
	}
	if req.AllowBorrow && len(req.Sponsors) > 0 {
		phases = append(phases, phase{owners: req.Sponsors, requireSame: true, sponsor: true})
		phases = append(phases, phase{owners: req.Sponsors, requireOther: true, sponsor: true})
	}
	return phases
}

func sortedEnvelopeStates(st *budget.BudgetState, flavor string) []*budget.EnvelopeState {
	var result []*budget.EnvelopeState
	for _, env := range st.Envelopes {
		if env.Spec.Flavor == flavor {
			result = append(result, env)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Spec.Name == result[j].Spec.Name {
			return false
		}
		return result[i].Spec.Name < result[j].Spec.Name
	})
	return result
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

func allowsBorrower(policy *v1.LendingPolicy, borrower string) bool {
	if policy == nil {
		return false
	}
	if len(policy.To) == 0 {
		return true
	}
	for _, pattern := range policy.To {
		if pattern == "*" {
			return true
		}
		if strings.HasSuffix(pattern, "*") {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(borrower, prefix) {
				return true
			}
			continue
		}
		if pattern == borrower {
			return true
		}
	}
	return false
}

type allocationTracker struct {
	expectedHoursPerGPU float64
	envAlloc            map[*budget.EnvelopeState]int32
	envHours            map[*budget.EnvelopeState]float64
	envBorrowed         map[*budget.EnvelopeState]int32
	envBorrowedHours    map[*budget.EnvelopeState]float64
	aggAlloc            map[*budget.AggregateState]int32
	aggHours            map[*budget.AggregateState]float64
}

func newAllocationTracker(duration time.Duration) *allocationTracker {
	hours := 0.0
	if duration > 0 {
		hours = duration.Hours()
	}
	return &allocationTracker{
		expectedHoursPerGPU: hours,
		envAlloc:            make(map[*budget.EnvelopeState]int32),
		envHours:            make(map[*budget.EnvelopeState]float64),
		envBorrowed:         make(map[*budget.EnvelopeState]int32),
		envBorrowedHours:    make(map[*budget.EnvelopeState]float64),
		aggAlloc:            make(map[*budget.AggregateState]int32),
		aggHours:            make(map[*budget.AggregateState]float64),
	}
}

func (a *allocationTracker) pendingAllocation(env *budget.EnvelopeState) int32 {
	return a.envAlloc[env]
}

func (a *allocationTracker) pendingAggregate(cap *budget.AggregateState) int32 {
	return a.aggAlloc[cap]
}

func (a *allocationTracker) pendingBorrowed(env *budget.EnvelopeState) int32 {
	return a.envBorrowed[env]
}

func (a *allocationTracker) pendingBorrowedHours(env *budget.EnvelopeState) float64 {
	return a.envBorrowedHours[env]
}

func (a *allocationTracker) allocate(env *budget.EnvelopeState, qty int32, sponsor bool) int32 {
	if qty <= 0 {
		return 0
	}
	a.envAlloc[env] += qty
	if a.expectedHoursPerGPU > 0 {
		a.envHours[env] += float64(qty) * a.expectedHoursPerGPU
	}
	for _, cap := range env.Aggregates {
		a.aggAlloc[cap] += qty
		if a.expectedHoursPerGPU > 0 {
			a.aggHours[cap] += float64(qty) * a.expectedHoursPerGPU
		}
	}
	if sponsor {
		a.envBorrowed[env] += qty
		if a.expectedHoursPerGPU > 0 {
			a.envBorrowedHours[env] += float64(qty) * a.expectedHoursPerGPU
		}
	}
	return qty
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

func min(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

// familyGraph tracks relationships for family sharing.
type familyGraph struct {
	parents  map[string]map[string]struct{}
	children map[string]map[string]struct{}
}

func newFamilyGraph() *familyGraph {
	return &familyGraph{
		parents:  make(map[string]map[string]struct{}),
		children: make(map[string]map[string]struct{}),
	}
}

func (g *familyGraph) addOwner(owner string) {
	if _, ok := g.parents[owner]; !ok {
		g.parents[owner] = make(map[string]struct{})
	}
	if _, ok := g.children[owner]; !ok {
		g.children[owner] = make(map[string]struct{})
	}
}

func (g *familyGraph) addEdge(parent, child string) {
	g.addOwner(parent)
	g.addOwner(child)
	g.children[parent][child] = struct{}{}
	g.parents[child][parent] = struct{}{}
}

func (g *familyGraph) parentsOf(owner string) []string {
	return keys(g.parents[owner])
}

func (g *familyGraph) siblings(owner string) []string {
	var result []string
	for parent := range g.parents[owner] {
		for child := range g.children[parent] {
			if child == owner {
				continue
			}
			result = append(result, child)
		}
	}
	return result
}

func (g *familyGraph) cousins(owner string) []string {
	var result []string
	for parent := range g.parents[owner] {
		for grand := range g.parents[parent] {
			for aunt := range g.children[grand] {
				if aunt == parent {
					continue
				}
				for cousin := range g.children[aunt] {
					result = append(result, cousin)
				}
			}
		}
	}
	sort.Strings(result)
	return result
}

func keys(m map[string]struct{}) []string {
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

// ValidateInventory ensures the inventory has at least one budget for the root owner.
func (inv *Inventory) ValidateInventory(owner string) error {
	if len(inv.owners[owner]) == 0 {
		return errors.New("owner has no budgets")
	}
	return nil
}

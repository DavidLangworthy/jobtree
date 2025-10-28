package forecast

import (
	"errors"
	"fmt"
	"sort"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/budget"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

const (
	// DefaultActivationLead is the conservative delay used when no precise time can be inferred.
	DefaultActivationLead = 15 * time.Minute
	// MinimumActivationLead ensures we never promise activation in the past.
	MinimumActivationLead = time.Minute
	// WindowActivationOffset accounts for binder jitter when aligning to an envelope window.
	WindowActivationOffset = 10 * time.Second
)

// Input captures the data needed to derive a reservation forecast.
type Input struct {
	Run          *v1.Run
	Now          time.Time
	Snapshot     *topology.Snapshot
	PackPlan     *pack.Plan
	PackErr      *pack.PlanError
	CoverErr     *cover.PlanError
	CoverRequest cover.Request
	BudgetStates []*budget.BudgetState
}

// Result contains the reservation plan emitted by the forecaster.
type Result struct {
	IntendedSlice  v1.IntendedSlice
	PayingEnvelope string
	EarliestStart  time.Time
	Forecast       v1.ReservationForecast
	Reason         string
}

// Plan determines how to represent a reservation for a run that cannot start immediately.
func Plan(in Input) (Result, error) {
	if in.Run == nil {
		return Result{}, errors.New("run must be provided")
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	scope := deriveScope(in)

	envelope, err := selectEnvelope(in, scope)
	if err != nil {
		return Result{}, err
	}

	intended := deriveSlice(in, scope)

	earliest := conservativeEarliest(in, envelope, scope)

	deficit := estimateDeficit(in)
	remedies := defaultRemedies()
	confidence := confidenceLabel(in)

	forecast := v1.ReservationForecast{
		DeficitGPUs: int32(deficit),
		Scope:       scope,
		Remedies:    remedies,
		Confidence:  confidence,
	}

	reason := buildReason(in, envelope, deficit)

	return Result{
		IntendedSlice:  intended,
		PayingEnvelope: envelope.Spec.Name,
		EarliestStart:  earliest,
		Forecast:       forecast,
		Reason:         reason,
	}, nil
}

func deriveScope(in Input) map[string]string {
	if in.CoverRequest.Location != nil && len(in.CoverRequest.Location) > 0 {
		return cloneMap(in.CoverRequest.Location)
	}
	if in.PackPlan != nil && len(in.PackPlan.Groups) > 0 {
		dom := in.PackPlan.Groups[0].Domain
		return map[string]string{
			topology.LabelRegion:       dom.Region,
			topology.LabelCluster:      dom.Cluster,
			topology.LabelFabricDomain: dom.Fabric,
		}
	}
	if in.Snapshot != nil {
		if dom, ok := in.Snapshot.LargestDomain(); ok {
			return map[string]string{
				topology.LabelRegion:       dom.Key.Region,
				topology.LabelCluster:      dom.Key.Cluster,
				topology.LabelFabricDomain: dom.Key.Fabric,
			}
		}
	}
	return map[string]string{}
}

func deriveSlice(in Input, scope map[string]string) v1.IntendedSlice {
	slice := v1.IntendedSlice{Domain: cloneMap(scope)}
	if in.PackPlan == nil {
		return slice
	}
	seen := map[string]struct{}{}
	for _, group := range in.PackPlan.Groups {
		for _, alloc := range group.NodePlacements {
			if _, ok := seen[alloc.Node]; ok {
				continue
			}
			seen[alloc.Node] = struct{}{}
			slice.Nodes = append(slice.Nodes, alloc.Node)
		}
	}
	sort.Strings(slice.Nodes)
	return slice
}

func conservativeEarliest(in Input, env *budget.EnvelopeState, scope map[string]string) time.Time {
	candidate := in.Now.Add(DefaultActivationLead)
	if env.Spec.Start != nil && in.Now.Before(env.Spec.Start.Time) {
		if env.Spec.PreActivation != nil && !env.Spec.PreActivation.AllowReservations {
			// Reservations not allowed before start; fall back to default lead to avoid promising the window.
			return candidate
		}
		start := env.Spec.Start.Time.Add(WindowActivationOffset)
		if start.Before(in.Now.Add(MinimumActivationLead)) {
			start = in.Now.Add(MinimumActivationLead)
		}
		return start
	}
	return candidate
}

func estimateDeficit(in Input) int {
	total := int(in.Run.Spec.Resources.TotalGPUs)
	if in.PackErr != nil {
		switch in.PackErr.Reason {
		case pack.FailureReasonInsufficientCapacity:
			free := 0
			if in.Snapshot != nil {
				free = in.Snapshot.TotalFreeGPUs()
			}
			if free >= total {
				return 0
			}
			return total - free
		case pack.FailureReasonInsufficientTopology:
			return total
		default:
			return total
		}
	}
	if in.CoverErr != nil {
		switch in.CoverErr.Reason {
		case cover.FailureReasonInsufficientCapacity:
			headroom := computeHeadroom(in)
			if headroom >= total {
				return 0
			}
			return total - headroom
		case cover.FailureReasonNoMatchingEnvelope:
			return total
		default:
			return total
		}
	}
	return total
}

func computeHeadroom(in Input) int {
	if len(in.BudgetStates) == 0 {
		return 0
	}
	remaining := 0
	for _, st := range in.BudgetStates {
		if st == nil || st.Budget == nil {
			continue
		}
		if st.Budget.Spec.Owner != in.Run.Spec.Owner {
			continue
		}
		for _, env := range st.Envelopes {
			if env.Spec.Flavor != in.Run.Spec.Resources.GPUType {
				continue
			}
			headroom := budget.EnvelopeHeadroom(env, budget.Usage{})
			if headroom.Concurrency > 0 {
				remaining += int(headroom.Concurrency)
			}
		}
	}
	return remaining
}

func selectEnvelope(in Input, scope map[string]string) (*budget.EnvelopeState, error) {
	candidates := []*budget.EnvelopeState{}
	for _, st := range in.BudgetStates {
		if st == nil || st.Budget == nil {
			continue
		}
		if st.Budget.Spec.Owner != in.Run.Spec.Owner {
			continue
		}
		for _, env := range st.Envelopes {
			if env.Spec.Flavor != in.Run.Spec.Resources.GPUType {
				continue
			}
			if len(scope) > 0 && !matchesScope(env.Spec.Selector, scope) {
				continue
			}
			candidates = append(candidates, env)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no matching envelopes found for run %s", in.Run.Name)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Spec.Name < candidates[j].Spec.Name
	})
	selected := candidates[0]
	if selected.Spec.Start != nil && in.Now.Before(selected.Spec.Start.Time) {
		if selected.Spec.PreActivation != nil && !selected.Spec.PreActivation.AllowReservations {
			return nil, fmt.Errorf("envelope %s does not allow reservations before start", selected.Spec.Name)
		}
	}
	return selected, nil
}

func matchesScope(selector map[string]string, scope map[string]string) bool {
	for key, value := range selector {
		if scopeValue, ok := scope[key]; ok {
			if scopeValue != value && value != "*" {
				return false
			}
		}
	}
	return true
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func defaultRemedies() []string {
	return []string{
		"Drop spares in scope",
		"Shrink elastic runs by step size",
		"Run fair lottery if deficit remains",
	}
}

func confidenceLabel(in Input) string {
	if in.CoverErr != nil && in.CoverErr.Reason == cover.FailureReasonNoMatchingEnvelope {
		return "window-aligned"
	}
	return "conservative"
}

func buildReason(in Input, env *budget.EnvelopeState, deficit int) string {
	if in.CoverErr != nil {
		switch in.CoverErr.Reason {
		case cover.FailureReasonNoMatchingEnvelope:
			if env.Spec.Start != nil && in.Now.Before(env.Spec.Start.Time) {
				return fmt.Sprintf("budget window opens at %s", env.Spec.Start.Time.Format(time.RFC3339))
			}
			return "no eligible envelope available"
		case cover.FailureReasonInsufficientCapacity:
			return fmt.Sprintf("budget headroom short by %d GPUs", deficit)
		case cover.FailureReasonACLRejected:
			return "borrowing policy rejected request"
		}
	}
	if in.PackErr != nil {
		switch in.PackErr.Reason {
		case pack.FailureReasonInsufficientCapacity:
			return fmt.Sprintf("cluster short by %d GPUs in scope", deficit)
		case pack.FailureReasonInsufficientTopology:
			return "no single domain satisfies run grouping"
		}
	}
	return "reservation pending"
}

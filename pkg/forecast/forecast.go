package forecast

import (
	"errors"
	"fmt"
	"sort"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

const (
	// DefaultActivationLead is the conservative base lead used when the
	// deficit is zero or unknown (e.g. a budget-window gate rather than a
	// capacity shortfall) — the floor the deficit-scaled estimate builds on.
	DefaultActivationLead = 15 * time.Minute
	// MinimumActivationLead ensures we never promise activation in the past.
	MinimumActivationLead = time.Minute
	// WindowActivationOffset accounts for binder jitter when aligning to an envelope window.
	WindowActivationOffset = 10 * time.Second
	// DeficitActivationLeadPerGPU scales the conservative estimate with the
	// size of the capacity shortfall: clearing a larger deficit structurally
	// requires the resolver to walk more reclaim phases (unfunded → spares →
	// shrink → lottery) and more candidate leases within each, so the
	// estimate grows with the deficit instead of promising the same fixed
	// window for a 1-GPU shortfall as for a 500-GPU one.
	DeficitActivationLeadPerGPU = 30 * time.Second
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
	Evaluation   *funding.Evaluation
	// Runs is the cluster's run set, used only to decide whether "shrink
	// elastic runs" is a real remedy (some malleable run of the same flavor
	// actually exists to shrink). Optional: when nil, that remedy is omitted
	// rather than guessed.
	Runs map[string]*v1.Run
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

	deficit := estimateDeficit(in)
	earliest := conservativeEarliest(in, envelope, scope, deficit)

	remedies := computeRemedies(in, scope)
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

// activationLeadForDeficit is the data-driven replacement for the flat
// DefaultActivationLead constant: the lead grows with the size of the
// capacity deficit instead of being identical for a 1-GPU and a 500-GPU
// shortfall, and is always clamped at MinimumActivationLead.
func activationLeadForDeficit(deficit int) time.Duration {
	lead := DefaultActivationLead
	if deficit > 0 {
		lead += time.Duration(deficit) * DeficitActivationLeadPerGPU
	}
	if lead < MinimumActivationLead {
		lead = MinimumActivationLead
	}
	return lead
}

func conservativeEarliest(in Input, env *funding.EnvelopeAccount, scope map[string]string, deficit int) time.Time {
	candidate := in.Now.Add(activationLeadForDeficit(deficit))
	if env.Spec.Start != nil && in.Now.Before(env.Spec.Start.Time) {
		if env.Spec.PreActivation != nil && !env.Spec.PreActivation.AllowReservations {
			// Reservations not allowed before start; fall back to the
			// deficit-scaled lead to avoid promising the window.
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
		case cover.FailureReasonBorrowLimit:
			headroom := computeHeadroom(in)
			borrowCap := 0
			if in.CoverRequest.MaxBorrowGPUs != nil {
				borrowCap = int(*in.CoverRequest.MaxBorrowGPUs)
			}
			capacity := headroom + borrowCap
			if capacity >= total {
				return 0
			}
			return total - capacity
		case cover.FailureReasonNoMatchingEnvelope:
			return total
		default:
			return total
		}
	}
	return total
}

// computeHeadroom asks the funding evaluation how much width the run's owner
// could still get funded on their own envelopes, ranked at the run's
// admission time. Family and sponsor capacity is deliberately excluded: the
// cover planner already failed to fill from those tiers, so counting them
// here would understate the deficit.
func computeHeadroom(in Input) int {
	if in.Evaluation == nil {
		return 0
	}
	admitted := in.CoverRequest.Admitted
	if admitted.IsZero() {
		admitted = in.Now
	}
	admission := in.Evaluation.NewAdmission(in.Run.Spec.Owner, admitted, in.CoverRequest.RunKey)
	remaining := 0
	for _, acct := range in.Evaluation.Envelopes() {
		if acct.Owner != in.Run.Spec.Owner {
			continue
		}
		if acct.Spec.Flavor != in.Run.Spec.Resources.GPUType {
			continue
		}
		if !windowAllowsAdmission(acct.Spec, in.Now) {
			continue
		}
		if width := admission.Available(acct.Key, false); width > 0 {
			admission.Take(acct.Key, width)
			remaining += int(width)
		}
	}
	return remaining
}

// windowAllowsAdmission mirrors the cover planner's admission gate: closed
// windows admit nothing, unopened windows only via preActivation.
func windowAllowsAdmission(env v1.BudgetEnvelope, now time.Time) bool {
	if env.Start != nil && now.Before(env.Start.Time) {
		return env.PreActivation != nil && env.PreActivation.AllowAdmission
	}
	if env.End != nil && !now.Before(env.End.Time) {
		return false
	}
	return true
}

func selectEnvelope(in Input, scope map[string]string) (*funding.EnvelopeAccount, error) {
	candidates := []*funding.EnvelopeAccount{}
	if in.Evaluation != nil {
		for _, acct := range in.Evaluation.Envelopes() {
			if acct.Owner != in.Run.Spec.Owner {
				continue
			}
			if acct.Spec.Flavor != in.Run.Spec.Resources.GPUType {
				continue
			}
			if len(scope) > 0 && !matchesScope(acct.Spec.Selector, scope) {
				continue
			}
			candidates = append(candidates, acct)
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

// computeRemedies replaces the old static defaultRemedies() constant: each
// structural step is included only when the real inputs show it would find
// something to work with. "Reclaim unfunded capacity" and "Drop spares"
// read the same funding.Evaluation the resolver itself consults (any
// envelope of the run's flavor currently carrying unfunded or spare width);
// "Shrink elastic runs" requires an actual malleable run of the same
// flavor to exist. The fair lottery always stays last: it is the resolver's
// backstop regardless of what structural cuts found. These signals are
// necessarily flavor-scoped (not location-scoped): forecast.Input carries
// the funding evaluation but not the per-node lease/class join the resolver
// itself uses, so this is a coarser but still real-data-derived read.
func computeRemedies(in Input, scope map[string]string) []string {
	var remedies []string
	if hasUnfundedCapacity(in) {
		remedies = append(remedies, "Reclaim unfunded capacity in scope")
	}
	if hasSpareCapacity(in) {
		remedies = append(remedies, "Drop spares in scope")
	}
	if hasMalleableRuns(in) {
		remedies = append(remedies, "Shrink elastic runs by step size")
	}
	remedies = append(remedies, "Run fair lottery if deficit remains")
	return remedies
}

func hasUnfundedCapacity(in Input) bool {
	if in.Evaluation == nil || in.Run == nil {
		return false
	}
	flavor := in.Run.Spec.Resources.GPUType
	for _, acct := range in.Evaluation.Envelopes() {
		if acct.Spec.Flavor != flavor {
			continue
		}
		if acct.WidthByClass[funding.ClassUnfunded] > 0 {
			return true
		}
	}
	return false
}

func hasSpareCapacity(in Input) bool {
	if in.Evaluation == nil || in.Run == nil {
		return false
	}
	flavor := in.Run.Spec.Resources.GPUType
	for _, acct := range in.Evaluation.Envelopes() {
		if acct.Spec.Flavor != flavor {
			continue
		}
		if acct.SpareWidth > 0 {
			return true
		}
	}
	return false
}

func hasMalleableRuns(in Input) bool {
	if in.Run == nil {
		return false
	}
	flavor := in.Run.Spec.Resources.GPUType
	for key, run := range in.Runs {
		if run == nil || run.Spec.Malleable == nil {
			continue
		}
		if run.Spec.Resources.GPUType != flavor {
			continue
		}
		if in.Run != nil && key == runKey(in.Run) {
			continue
		}
		return true
	}
	return false
}

func runKey(run *v1.Run) string {
	if run.Namespace == "" {
		return run.Name
	}
	return run.Namespace + "/" + run.Name
}

func confidenceLabel(in Input) string {
	if in.CoverErr != nil && in.CoverErr.Reason == cover.FailureReasonNoMatchingEnvelope {
		return "window-aligned"
	}
	return "conservative"
}

func buildReason(in Input, env *funding.EnvelopeAccount, deficit int) string {
	if in.CoverErr != nil {
		switch in.CoverErr.Reason {
		case cover.FailureReasonNoMatchingEnvelope:
			if env.Spec.Start != nil && in.Now.Before(env.Spec.Start.Time) {
				return fmt.Sprintf("budget window opens at %s", env.Spec.Start.Time.Format(time.RFC3339))
			}
			return "no eligible envelope available"
		case cover.FailureReasonInsufficientCapacity:
			return fmt.Sprintf("budget headroom short by %d GPUs", deficit)
		case cover.FailureReasonBorrowLimit:
			limit := "configured borrow limit"
			if in.CoverRequest.MaxBorrowGPUs != nil {
				limit = fmt.Sprintf("borrow limit of %d GPUs", *in.CoverRequest.MaxBorrowGPUs)
			}
			return fmt.Sprintf("%s exhausted for requested width", limit)
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

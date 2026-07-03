package funding

import (
	"time"
)

// Admission is a planning scratchpad over one evaluation: the cover planner
// allocates width envelope by envelope, and pending takes must count
// against shared aggregate caps and lending pools before anything is
// materialized. It never mutates the evaluation.
type Admission struct {
	ev       *Evaluation
	owner    string
	admitted time.Time
	name     string

	envPending map[EnvelopeKey]int32
	aggPending map[*aggregateAccount]int32
}

// NewAdmission starts planning a claim for owner, ranked at its run's
// admission time and key (a growing run keeps the rank of its original
// admission). The key is the prospective run's namespaced key; it drives the
// deterministic name tiebreak among same-second, same-tier peers. Pass "" to
// take the conservative estimate when no run identity is available.
func (ev *Evaluation) NewAdmission(owner string, admitted time.Time, name string) *Admission {
	if admitted.IsZero() {
		admitted = ev.Now
	}
	return &Admission{
		ev:         ev,
		owner:      owner,
		admitted:   admitted,
		name:       name,
		envPending: make(map[EnvelopeKey]int32),
		aggPending: make(map[*aggregateAccount]int32),
	}
}

// Available reports the width still grantable on this envelope, net of
// everything already taken in this admission. Every AvailableWidth bound is
// width-denominated and an envelope's takes within one admission are
// uniformly sponsor or family (the relationship is fixed per envelope and
// owner), so pending width subtracts from all of them alike.
func (a *Admission) Available(key EnvelopeKey, sponsor bool) int32 {
	acct := a.ev.envelopes[key]
	if acct == nil {
		return 0
	}
	available := a.ev.AvailableWidth(key, a.owner, a.admitted, a.name, sponsor)
	available -= a.envPending[key]
	for _, agg := range acct.aggregates {
		if pending := a.aggPending[agg]; pending > 0 {
			if room := a.ev.aggregateAvailable(agg) - pending; room < available {
				available = room
			}
		}
	}
	if available < 0 {
		return 0
	}
	return available
}

// Take records a grant of width on the envelope. Callers must not take more
// than Available reported.
func (a *Admission) Take(key EnvelopeKey, width int32) {
	if width <= 0 {
		return
	}
	a.envPending[key] += width
	if acct := a.ev.envelopes[key]; acct != nil {
		for _, agg := range acct.aggregates {
			a.aggPending[agg] += width
		}
	}
}

// aggregateAvailable is the aggregate's remaining width for new claims:
// conservative (no recall through aggregates), both dimensions.
func (ev *Evaluation) aggregateAvailable(agg *aggregateAccount) int32 {
	available := int32(1 << 30)
	width := ev.aggWidth[agg]
	if agg.spec.MaxConcurrency != nil {
		if room := *agg.spec.MaxConcurrency - width; room < available {
			available = room
		}
	}
	if agg.spec.MaxGPUHours != nil {
		period := ev.Period.Hours()
		if period > 0 {
			remaining := float64(*agg.spec.MaxGPUHours) - agg.consumed - float64(width)*period
			if remaining < 0 {
				remaining = 0
			}
			if room := int32(remaining / period); room < available {
				available = room
			}
		}
	}
	if available < 0 {
		return 0
	}
	return available
}

// AggregateUsage is a reporting view of one aggregate cap.
type AggregateUsage struct {
	Name             string
	Flavor           string
	FundedWidth      int32
	ConsumedGPUHours float64
	MaxConcurrency   *int32
	MaxGPUHours      *int64
}

// Aggregates reports the budget's aggregate caps in declaration order.
func (ev *Evaluation) Aggregates(budget string) []AggregateUsage {
	seen := make(map[*aggregateAccount]bool)
	var out []AggregateUsage
	for _, acct := range ev.Envelopes() {
		if acct.Key.Budget != budget {
			continue
		}
		for _, agg := range acct.aggregates {
			if seen[agg] {
				continue
			}
			seen[agg] = true
			out = append(out, AggregateUsage{
				Name:             agg.spec.Name,
				Flavor:           agg.spec.Flavor,
				FundedWidth:      ev.aggWidth[agg],
				ConsumedGPUHours: agg.consumed,
				MaxConcurrency:   agg.spec.MaxConcurrency,
				MaxGPUHours:      agg.spec.MaxGPUHours,
			})
		}
	}
	return out
}

package v1

import (
	"fmt"
	"time"
)

// Budget represents the allocation envelopes that constrain Runs.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Envelopes",type=integer,JSONPath=`length(.spec.envelopes)`
type Budget struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata,omitempty"`

	Spec   BudgetSpec   `json:"spec,omitempty"`
	Status BudgetStatus `json:"status,omitempty"`
}

// BudgetSpec describes available resource envelopes for a particular owner.
type BudgetSpec struct {
	Owner         string             `json:"owner"`
	Envelopes     []BudgetEnvelope   `json:"envelopes"`
	AggregateCaps []AggregateCap     `json:"aggregateCaps,omitempty"`
	Parents       []string           `json:"parents,omitempty"`
	AutoRenew     *AutoRenewSchedule `json:"autoRenew,omitempty"`
}

// AutoRenewSchedule defines reporting rotation for open-ended envelopes.
type AutoRenewSchedule struct {
	Period       Duration `json:"period"`
	NotifyBefore Duration `json:"notifyBefore"`
}

// BudgetEnvelope defines a location/time scoped limit.
type BudgetEnvelope struct {
	Name          string               `json:"name"`
	Flavor        string               `json:"flavor"`
	Selector      map[string]string    `json:"selector"`
	Concurrency   int32                `json:"concurrency"`
	MaxGPUHours   *int64               `json:"maxGPUHours,omitempty"`
	Start         *Time                `json:"start,omitempty"`
	End           *Time                `json:"end,omitempty"`
	PreActivation *PreActivationPolicy `json:"preActivation,omitempty"`
	Lending       *LendingPolicy       `json:"lending,omitempty"`
}

// PreActivationPolicy controls reservation/admission before start.
type PreActivationPolicy struct {
	AllowReservations bool `json:"allowReservations"`
	AllowAdmission    bool `json:"allowAdmission"`
}

// LendingPolicy specifies optional lending configuration.
type LendingPolicy struct {
	Allow          bool     `json:"allow"`
	To             []string `json:"to,omitempty"`
	MaxConcurrency *int32   `json:"maxConcurrency,omitempty"`
	MaxGPUHours    *int64   `json:"maxGPUHours,omitempty"`
}

// AggregateCap bounds the sum across envelopes.
type AggregateCap struct {
	Name           string   `json:"name"`
	Flavor         string   `json:"flavor"`
	Envelopes      []string `json:"envelopes"`
	MaxConcurrency *int32   `json:"maxConcurrency,omitempty"`
	MaxGPUHours    *int64   `json:"maxGPUHours,omitempty"`
}

// BudgetStatus surfaces derived accounting data.
type BudgetStatus struct {
        ObservedGeneration int64              `json:"observedGeneration,omitempty"`
        Headroom           []EnvelopeHeadroom `json:"headroom,omitempty"`
        AggregateHeadroom  []AggregateHeadroom `json:"aggregateHeadroom,omitempty"`
        UpdatedAt          *Time              `json:"updatedAt,omitempty"`
}

// EnvelopeHeadroom reports remaining capacity for an envelope.
type EnvelopeHeadroom struct {
        Name        string `json:"name"`
        Flavor      string `json:"flavor"`
        Concurrency int32  `json:"concurrency"`
        GPUHours    *int64 `json:"gpuHours,omitempty"`
}

// AggregateHeadroom reports remaining capacity for an aggregate cap.
type AggregateHeadroom struct {
        Name        string  `json:"name"`
        Flavor      string  `json:"flavor"`
        Concurrency *int32  `json:"concurrency,omitempty"`
        GPUHours    *int64  `json:"gpuHours,omitempty"`
}

// BudgetList contains a list of Budgets.
// +kubebuilder:object:root=true
type BudgetList struct {
	TypeMeta `json:",inline"`
	ListMeta `json:"metadata,omitempty"`
	Items    []Budget `json:"items"`
}

// ValidateCreate implements webhook.Validator.
func (b *Budget) ValidateCreate() error {
	return b.validate()
}

// ValidateUpdate implements webhook.Validator.
func (b *Budget) ValidateUpdate(RuntimeObject) error {
	return b.validate()
}

// ValidateDelete implements webhook.Validator.
func (b *Budget) ValidateDelete() error {
	return nil
}

func (b *Budget) validate() error {
	if b.Spec.Owner == "" {
		return fmt.Errorf("spec.owner is required")
	}
	if len(b.Spec.Envelopes) == 0 {
		return fmt.Errorf("spec.envelopes must not be empty")
	}
	for i := range b.Spec.Envelopes {
		if err := b.Spec.Envelopes[i].Validate(); err != nil {
			return fmt.Errorf("envelope[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate ensures the envelope has sane configuration.
func (e *BudgetEnvelope) Validate() error {
	if e.Name == "" {
		return fmt.Errorf("name is required")
	}
	if e.Flavor == "" {
		return fmt.Errorf("flavor is required")
	}
	if len(e.Selector) == 0 {
		return fmt.Errorf("selector must contain at least one label")
	}
	if e.Concurrency <= 0 {
		return fmt.Errorf("concurrency must be positive")
	}
	if e.Start != nil && e.End != nil {
		if !e.End.After(*e.Start) {
			return fmt.Errorf("end must be after start")
		}
		if e.MaxGPUHours != nil {
			dur := e.End.Sub(*e.Start)
			max := float64(e.Concurrency) * dur.Hours()
			if float64(*e.MaxGPUHours) > max+1e-6 {
				return fmt.Errorf("maxGPUHours exceeds concurrency×window")
			}
		}
	}
	if e.MaxGPUHours != nil && e.Start == nil && e.End == nil {
		if *e.MaxGPUHours < 0 {
			return fmt.Errorf("maxGPUHours must be non-negative")
		}
	}
	if e.Lending != nil {
		if err := e.Lending.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate validates lending constraints.
func (l *LendingPolicy) Validate() error {
	if !l.Allow {
		return nil
	}
	if l.MaxConcurrency != nil && *l.MaxConcurrency <= 0 {
		return fmt.Errorf("lending.maxConcurrency must be positive when set")
	}
	if l.MaxGPUHours != nil && *l.MaxGPUHours < 0 {
		return fmt.Errorf("lending.maxGPUHours must be non-negative")
	}
	return nil
}

// DeepCopyInto deep copies the receiver into out.
func (in *Budget) DeepCopyInto(out *Budget) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = *in.Spec.DeepCopy()
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function.
func (in *Budget) DeepCopy() *Budget {
	if in == nil {
		return nil
	}
	out := new(Budget)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *Budget) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto deep copies the receiver into out.
func (in *BudgetList) DeepCopyInto(out *BudgetList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Budget, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function.
func (in *BudgetList) DeepCopy() *BudgetList {
	if in == nil {
		return nil
	}
	out := new(BudgetList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *BudgetList) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopy creates a deepcopy of the spec.
func (in *BudgetSpec) DeepCopy() *BudgetSpec {
	if in == nil {
		return nil
	}
	out := new(BudgetSpec)
	*out = *in
	if in.Envelopes != nil {
		out.Envelopes = make([]BudgetEnvelope, len(in.Envelopes))
		for i := range in.Envelopes {
			out.Envelopes[i] = *in.Envelopes[i].DeepCopy()
		}
	}
	if in.AggregateCaps != nil {
		out.AggregateCaps = make([]AggregateCap, len(in.AggregateCaps))
		copy(out.AggregateCaps, in.AggregateCaps)
	}
	if in.Parents != nil {
		out.Parents = append([]string{}, in.Parents...)
	}
	if in.AutoRenew != nil {
		out.AutoRenew = &AutoRenewSchedule{Period: in.AutoRenew.Period, NotifyBefore: in.AutoRenew.NotifyBefore}
	}
	return out
}

// DeepCopy creates a deepcopy of the envelope.
func (in *BudgetEnvelope) DeepCopy() *BudgetEnvelope {
	if in == nil {
		return nil
	}
	out := new(BudgetEnvelope)
	*out = *in
	if in.Selector != nil {
		out.Selector = make(map[string]string, len(in.Selector))
		for k, v := range in.Selector {
			out.Selector[k] = v
		}
	}
	if in.MaxGPUHours != nil {
		val := *in.MaxGPUHours
		out.MaxGPUHours = &val
	}
	if in.Start != nil {
		start := in.Start.DeepCopy()
		out.Start = &start
	}
	if in.End != nil {
		end := in.End.DeepCopy()
		out.End = &end
	}
	if in.PreActivation != nil {
		pa := *in.PreActivation
		out.PreActivation = &pa
	}
	if in.Lending != nil {
		out.Lending = in.Lending.DeepCopy()
	}
	return out
}

// DeepCopy creates a deepcopy of the lending policy.
func (in *LendingPolicy) DeepCopy() *LendingPolicy {
	if in == nil {
		return nil
	}
	out := new(LendingPolicy)
	*out = *in
	if in.To != nil {
		out.To = append([]string{}, in.To...)
	}
	if in.MaxConcurrency != nil {
		val := *in.MaxConcurrency
		out.MaxConcurrency = &val
	}
	if in.MaxGPUHours != nil {
		val := *in.MaxGPUHours
		out.MaxGPUHours = &val
	}
	return out
}

// ValidateMaxHoursWindow validates integral limit for given window.
func ValidateMaxHoursWindow(concurrency int32, window time.Duration, hours *int64) error {
	if hours == nil {
		return nil
	}
	if *hours < 0 {
		return fmt.Errorf("maxGPUHours must be non-negative")
	}
	limit := float64(concurrency) * window.Hours()
	if float64(*hours) > limit+1e-6 {
		return fmt.Errorf("maxGPUHours exceeds concurrency×window")
	}
	return nil
}

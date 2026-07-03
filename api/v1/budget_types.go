package v1

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Printer columns must be simple scalar JSON paths; the API server rejects
// function expressions like length(). (Detached comment: doc-block text would
// leak into the CRD description.)

// Budget represents the allocation envelopes that constrain Runs.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Updated",type=date,JSONPath=`.status.updatedAt`
type Budget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

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
	Period       metav1.Duration `json:"period"`
	NotifyBefore metav1.Duration `json:"notifyBefore"`
}

// Envelope sharing modes: family sharing of excess needs no lending policy
// (quota-semantics.md Decision 2); "none" opts the envelope out of family
// excess entirely. Sharing never affects the owner's own use or the lending
// policy for sponsors.
const (
	SharingFamily = "family"
	SharingNone   = "none"
)

// BudgetEnvelope defines a location/time scoped limit.
type BudgetEnvelope struct {
	Name        string            `json:"name"`
	Flavor      string            `json:"flavor"`
	Selector    map[string]string `json:"selector"`
	Concurrency int32             `json:"concurrency"`
	MaxGPUHours *int64            `json:"maxGPUHours,omitempty"`
	Start       *metav1.Time      `json:"start,omitempty"`
	End         *metav1.Time      `json:"end,omitempty"`
	// Sharing controls family access to this envelope's excess: "" or
	// "family" allows it, "none" opts out.
	// +kubebuilder:validation:Enum="";family;none
	Sharing       string               `json:"sharing,omitempty"`
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
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
	Headroom           []EnvelopeHeadroom  `json:"headroom,omitempty"`
	AggregateHeadroom  []AggregateHeadroom `json:"aggregateHeadroom,omitempty"`
	Usage              []EnvelopeUsage     `json:"usage,omitempty"`
	UpdatedAt          *metav1.Time        `json:"updatedAt,omitempty"`
}

// EnvelopeUsage reports an envelope's width split by derived funding class
// (R14/R15) plus the spare-role width, and its consumed integral. Unfunded
// width names this envelope as payer but is never charged against its caps.
type EnvelopeUsage struct {
	Name             string  `json:"name"`
	Flavor           string  `json:"flavor"`
	OwnedGPUs        int32   `json:"ownedGPUs,omitempty"`
	SharedGPUs       int32   `json:"sharedGPUs,omitempty"`
	BorrowedGPUs     int32   `json:"borrowedGPUs,omitempty"`
	UnfundedGPUs     int32   `json:"unfundedGPUs,omitempty"`
	SpareGPUs        int32   `json:"spareGPUs,omitempty"`
	ConsumedGPUHours float64 `json:"consumedGPUHours,omitempty"`
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
	Name        string `json:"name"`
	Flavor      string `json:"flavor"`
	Concurrency *int32 `json:"concurrency,omitempty"`
	GPUHours    *int64 `json:"gpuHours,omitempty"`
}

// BudgetList contains a list of Budgets.
// +kubebuilder:object:root=true
type BudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Budget `json:"items"`
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
	envelopeNames := make(map[string]struct{}, len(b.Spec.Envelopes))
	for i := range b.Spec.Envelopes {
		if err := b.Spec.Envelopes[i].Validate(); err != nil {
			return fmt.Errorf("envelope[%d]: %w", i, err)
		}
		name := b.Spec.Envelopes[i].Name
		if _, dup := envelopeNames[name]; dup {
			return fmt.Errorf("envelope[%d]: duplicate envelope name %q", i, name)
		}
		envelopeNames[name] = struct{}{}
	}
	capNames := make(map[string]struct{}, len(b.Spec.AggregateCaps))
	for i := range b.Spec.AggregateCaps {
		cap := &b.Spec.AggregateCaps[i]
		if err := cap.validate(envelopeNames); err != nil {
			return fmt.Errorf("aggregateCap[%d]: %w", i, err)
		}
		if _, dup := capNames[cap.Name]; dup {
			return fmt.Errorf("aggregateCap[%d]: duplicate aggregate cap name %q", i, cap.Name)
		}
		capNames[cap.Name] = struct{}{}
	}
	return nil
}

// validate checks the cap against the budget's declared envelope names.
func (a *AggregateCap) validate(declared map[string]struct{}) error {
	if a.Name == "" {
		return fmt.Errorf("name is required")
	}
	if a.Flavor == "" {
		return fmt.Errorf("flavor is required")
	}
	if len(a.Envelopes) == 0 {
		return fmt.Errorf("envelopes must reference at least one envelope")
	}
	seen := make(map[string]struct{}, len(a.Envelopes))
	for _, ref := range a.Envelopes {
		if _, ok := declared[ref]; !ok {
			return fmt.Errorf("references unknown envelope %q", ref)
		}
		if _, dup := seen[ref]; dup {
			return fmt.Errorf("references envelope %q more than once", ref)
		}
		seen[ref] = struct{}{}
	}
	if a.MaxConcurrency != nil && *a.MaxConcurrency <= 0 {
		return fmt.Errorf("maxConcurrency must be positive when set")
	}
	if a.MaxGPUHours != nil && *a.MaxGPUHours < 0 {
		return fmt.Errorf("maxGPUHours must be non-negative")
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
	if e.Sharing != "" && e.Sharing != SharingFamily && e.Sharing != SharingNone {
		return fmt.Errorf("sharing must be %q or %q when set", SharingFamily, SharingNone)
	}
	if e.Start != nil && e.End != nil {
		if !e.End.Time.After(e.Start.Time) {
			return fmt.Errorf("end must be after start")
		}
		if e.MaxGPUHours != nil {
			dur := e.End.Time.Sub(e.Start.Time)
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

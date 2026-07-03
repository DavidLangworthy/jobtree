package v1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Run captures the researcher-friendly request.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="GPUs",type=integer,JSONPath=`.spec.resources.totalGPUs`
type Run struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunSpec   `json:"spec,omitempty"`
	Status RunStatus `json:"status,omitempty"`
}

// RunSpec defines the desired Run behavior.
type RunSpec struct {
	Owner     string           `json:"owner"`
	Resources RunResources     `json:"resources"`
	Locality  *RunLocality     `json:"locality,omitempty"`
	Runtime   *RunRuntime      `json:"runtime,omitempty"`
	Malleable *RunMalleability `json:"malleable,omitempty"`
	Funding   *RunFunding      `json:"funding,omitempty"`
	Spares    *int32           `json:"sparesPerGroup,omitempty"`
}

// RunResources describes GPU requirements.
type RunResources struct {
	GPUType   string `json:"gpuType"`
	TotalGPUs int32  `json:"totalGPUs"`
}

// RunLocality captures placement preferences.
type RunLocality struct {
	GroupGPUs             *int32 `json:"groupGPUs,omitempty"`
	AllowCrossGroupSpread *bool  `json:"allowCrossGroupSpread,omitempty"`
}

// RunRuntime covers runtime behavior hints.
type RunRuntime struct {
	Checkpoint metav1.Duration `json:"checkpoint,omitempty"`
}

// RunMalleability allows elastic scaling.
type RunMalleability struct {
	MinTotalGPUs     int32  `json:"minTotalGPUs"`
	MaxTotalGPUs     int32  `json:"maxTotalGPUs"`
	StepGPUs         int32  `json:"stepGPUs"`
	DesiredTotalGPUs *int32 `json:"desiredTotalGPUs,omitempty"`
}

// RunFunding captures borrowing intents.
type RunFunding struct {
	AllowBorrow   bool     `json:"allowBorrow"`
	MaxBorrowGPUs *int32   `json:"maxBorrowGPUs,omitempty"`
	Sponsors      []string `json:"sponsors,omitempty"`
}

// RunStatus reports lifecycle information.
type RunStatus struct {
	Phase              string            `json:"phase,omitempty"`
	Message            string            `json:"message,omitempty"`
	Generation         int64             `json:"generation,omitempty"`
	PendingReservation *string           `json:"pendingReservation,omitempty"`
	EarliestStart      *metav1.Time      `json:"earliestStart,omitempty"`
	Width              *RunWidthStatus   `json:"width,omitempty"`
	Funding            *RunFundingStatus `json:"funding,omitempty"`
}

// RunWidthStatus summarises elastic width bookkeeping.
type RunWidthStatus struct {
	Min       int32  `json:"min,omitempty"`
	Max       int32  `json:"max,omitempty"`
	Desired   int32  `json:"desired,omitempty"`
	Allocated int32  `json:"allocated,omitempty"`
	Pending   string `json:"pending,omitempty"`
}

// RunFundingStatus reports the derived funding classification (R14/R15):
// current width and accrued GPU-hours per class. Classes are computed by the
// funding derivation from budgets, leases, and the clock — they are status
// only and never feed back into evaluation.
type RunFundingStatus struct {
	OwnedGPUs        int32                   `json:"ownedGPUs,omitempty"`
	OwnedGPUHours    float64                 `json:"ownedGPUHours,omitempty"`
	SharedGPUs       int32                   `json:"sharedGPUs,omitempty"`
	SharedGPUHours   float64                 `json:"sharedGPUHours,omitempty"`
	BorrowedGPUs     int32                   `json:"borrowedGPUs,omitempty"`
	BorrowedGPUHours float64                 `json:"borrowedGPUHours,omitempty"`
	UnfundedGPUs     int32                   `json:"unfundedGPUs,omitempty"`
	UnfundedGPUHours float64                 `json:"unfundedGPUHours,omitempty"`
	Lenders          []RunFundingLenderShare `json:"lenders,omitempty"`
}

// RunFundingLenderShare attributes shared or borrowed capacity to the owner
// whose envelope funds it (family lender or sponsor).
type RunFundingLenderShare struct {
	Owner    string  `json:"owner"`
	GPUs     int32   `json:"gpus,omitempty"`
	GPUHours float64 `json:"gpuHours,omitempty"`
}

// RunList contains a list of Run.
// +kubebuilder:object:root=true
type RunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Run `json:"items"`
}

// AllowCrossGroupSpread returns the effective cross-group-spread setting,
// applying the API default (true) when the field is unset. Consumers must
// use this instead of re-implementing the default.
func (s *RunSpec) AllowCrossGroupSpread() bool {
	if s.Locality == nil || s.Locality.AllowCrossGroupSpread == nil {
		return true
	}
	return *s.Locality.AllowCrossGroupSpread
}

// Desired returns the effective desired width, applying the API default
// (MaxTotalGPUs) when the field is unset. Consumers must use this instead
// of re-implementing the default.
func (m *RunMalleability) Desired() int32 {
	if m.DesiredTotalGPUs != nil {
		return *m.DesiredTotalGPUs
	}
	return m.MaxTotalGPUs
}

// Default implements webhook.Defaulter, persisting the effective defaults.
func (r *Run) Default() {
	if r.Spec.Locality == nil {
		r.Spec.Locality = &RunLocality{}
	}
	if r.Spec.Locality.AllowCrossGroupSpread == nil {
		value := r.Spec.AllowCrossGroupSpread()
		r.Spec.Locality.AllowCrossGroupSpread = &value
	}
	if r.Spec.Malleable != nil && r.Spec.Malleable.DesiredTotalGPUs == nil {
		desired := r.Spec.Malleable.Desired()
		r.Spec.Malleable.DesiredTotalGPUs = &desired
	}
}

// ValidateCreate implements webhook.Validator.
func (r *Run) ValidateCreate() error {
	return r.validate()
}

// ValidateUpdate implements webhook.Validator.
func (r *Run) ValidateUpdate(RuntimeObject) error {
	return r.validate()
}

// ValidateDelete implements webhook.Validator.
func (r *Run) ValidateDelete() error {
	return nil
}

func (r *Run) validate() error {
	if r.Spec.Owner == "" {
		return fmt.Errorf("spec.owner is required")
	}
	if r.Spec.Resources.GPUType == "" {
		return fmt.Errorf("spec.resources.gpuType is required")
	}
	if r.Spec.Resources.TotalGPUs <= 0 {
		return fmt.Errorf("spec.resources.totalGPUs must be positive")
	}
	if r.Spec.Locality != nil && r.Spec.Locality.GroupGPUs != nil {
		if *r.Spec.Locality.GroupGPUs <= 0 {
			return fmt.Errorf("spec.locality.groupGPUs must be positive when set")
		}
	}
	if r.Spec.Malleable != nil {
		m := r.Spec.Malleable
		if m.MinTotalGPUs <= 0 || m.MaxTotalGPUs <= 0 {
			return fmt.Errorf("malleable min/max must be positive")
		}
		if m.StepGPUs <= 0 {
			return fmt.Errorf("malleable.stepGPUs must be positive")
		}
		if m.MinTotalGPUs > m.MaxTotalGPUs {
			return fmt.Errorf("malleable.minTotalGPUs must be <= maxTotalGPUs")
		}
		if r.Spec.Resources.TotalGPUs < m.MinTotalGPUs || r.Spec.Resources.TotalGPUs > m.MaxTotalGPUs {
			return fmt.Errorf("resources.totalGPUs must fall within malleable min/max")
		}
		if (r.Spec.Resources.TotalGPUs-m.MinTotalGPUs)%m.StepGPUs != 0 {
			return fmt.Errorf("resources.totalGPUs must align with malleable.stepGPUs")
		}
		if m.DesiredTotalGPUs != nil {
			desired := *m.DesiredTotalGPUs
			if desired < m.MinTotalGPUs || desired > m.MaxTotalGPUs {
				return fmt.Errorf("malleable.desiredTotalGPUs must fall within min/max")
			}
			if (desired-m.MinTotalGPUs)%m.StepGPUs != 0 {
				return fmt.Errorf("malleable.desiredTotalGPUs must align with stepGPUs")
			}
		}
	}
	if r.Spec.Funding != nil {
		if err := r.Spec.Funding.Validate(); err != nil {
			return err
		}
	}
	if r.Spec.Spares != nil {
		if *r.Spec.Spares < 0 {
			return fmt.Errorf("sparesPerGroup must be >= 0 when set")
		}
	}
	return nil
}

// Validate ensures the funding section is consistent.
func (f *RunFunding) Validate() error {
	if !f.AllowBorrow {
		return nil
	}
	if f.MaxBorrowGPUs != nil && *f.MaxBorrowGPUs <= 0 {
		return fmt.Errorf("funding.maxBorrowGPUs must be positive when set")
	}
	return nil
}

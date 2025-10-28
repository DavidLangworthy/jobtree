package v1

import "fmt"

// Run captures the researcher-friendly request.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="GPUs",type=integer,JSONPath=`.spec.resources.totalGPUs`
type Run struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata,omitempty"`

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
	Checkpoint Duration `json:"checkpoint,omitempty"`
}

// RunMalleability allows elastic scaling.
type RunMalleability struct {
	MinTotalGPUs int32 `json:"minTotalGPUs"`
	MaxTotalGPUs int32 `json:"maxTotalGPUs"`
	StepGPUs     int32 `json:"stepGPUs"`
}

// RunFunding captures borrowing intents.
type RunFunding struct {
	AllowBorrow   bool     `json:"allowBorrow"`
	MaxBorrowGPUs *int32   `json:"maxBorrowGPUs,omitempty"`
	Sponsors      []string `json:"sponsors,omitempty"`
}

// RunStatus reports lifecycle information.
type RunStatus struct {
	Phase              string  `json:"phase,omitempty"`
	Message            string  `json:"message,omitempty"`
	Generation         int64   `json:"generation,omitempty"`
	PendingReservation *string `json:"pendingReservation,omitempty"`
	EarliestStart      *Time   `json:"earliestStart,omitempty"`
}

// RunList contains a list of Run.
// +kubebuilder:object:root=true
type RunList struct {
	TypeMeta `json:",inline"`
	ListMeta `json:"metadata,omitempty"`
	Items    []Run `json:"items"`
}

// Default implements webhook.Defaulter.
func (r *Run) Default() {
	if r.Spec.Locality == nil {
		r.Spec.Locality = &RunLocality{}
	}
	if r.Spec.Locality.AllowCrossGroupSpread == nil {
		value := true
		r.Spec.Locality.AllowCrossGroupSpread = &value
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
	}
	if r.Spec.Funding != nil {
		if err := r.Spec.Funding.Validate(); err != nil {
			return err
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

// DeepCopyInto deep copies the Run.
func (in *Run) DeepCopyInto(out *Run) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = *in.Spec.DeepCopy()
	out.Status = *in.Status.DeepCopy()
}

// DeepCopy Deep copies the Run.
func (in *Run) DeepCopy() *Run {
	if in == nil {
		return nil
	}
	out := new(Run)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject deep copies runtime object.
func (in *Run) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto deep copies the RunList.
func (in *RunList) DeepCopyInto(out *RunList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Run, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy creates a deep copy of RunStatus.
func (in *RunStatus) DeepCopy() *RunStatus {
	if in == nil {
		return nil
	}
	out := new(RunStatus)
	*out = *in
	if in.PendingReservation != nil {
		value := *in.PendingReservation
		out.PendingReservation = &value
	}
	if in.EarliestStart != nil {
		value := in.EarliestStart.DeepCopy()
		out.EarliestStart = &value
	}
	return out
}

// DeepCopy deep copies RunList.
func (in *RunList) DeepCopy() *RunList {
	if in == nil {
		return nil
	}
	out := new(RunList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject deep copies runtime object.
func (in *RunList) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopy copies RunSpec.
func (in *RunSpec) DeepCopy() *RunSpec {
	if in == nil {
		return nil
	}
	out := new(RunSpec)
	*out = *in
	if in.Locality != nil {
		out.Locality = in.Locality.DeepCopy()
	}
	if in.Runtime != nil {
		out.Runtime = &RunRuntime{Checkpoint: in.Runtime.Checkpoint}
	}
	if in.Malleable != nil {
		copy := *in.Malleable
		out.Malleable = &copy
	}
	if in.Funding != nil {
		out.Funding = in.Funding.DeepCopy()
	}
	return out
}

// DeepCopy copies RunLocality.
func (in *RunLocality) DeepCopy() *RunLocality {
	if in == nil {
		return nil
	}
	out := new(RunLocality)
	*out = *in
	if in.GroupGPUs != nil {
		value := *in.GroupGPUs
		out.GroupGPUs = &value
	}
	if in.AllowCrossGroupSpread != nil {
		value := *in.AllowCrossGroupSpread
		out.AllowCrossGroupSpread = &value
	}
	return out
}

// DeepCopy copies funding.
func (in *RunFunding) DeepCopy() *RunFunding {
	if in == nil {
		return nil
	}
	out := new(RunFunding)
	*out = *in
	if in.MaxBorrowGPUs != nil {
		value := *in.MaxBorrowGPUs
		out.MaxBorrowGPUs = &value
	}
	if in.Sponsors != nil {
		out.Sponsors = append([]string{}, in.Sponsors...)
	}
	return out
}

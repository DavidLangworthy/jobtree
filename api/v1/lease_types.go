package v1

import (
	"fmt"
	"reflect"
)

// Lease records immutable consumption facts.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.runRef.name`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.slice.role`
// +kubebuilder:printcolumn:name="Start",type=string,JSONPath=`.spec.interval.start`
type Lease struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata,omitempty"`

	Spec   LeaseSpec   `json:"spec,omitempty"`
	Status LeaseStatus `json:"status,omitempty"`
}

// LeaseSpec is immutable after creation.
type LeaseSpec struct {
	Owner          string        `json:"owner"`
	RunRef         RunReference  `json:"runRef"`
	CompPath       []string      `json:"compPath,omitempty"`
	Slice          LeaseSlice    `json:"slice"`
	Interval       LeaseInterval `json:"interval"`
	PaidByEnvelope string        `json:"paidByEnvelope"`
	Reason         string        `json:"reason"`
}

// LeaseSlice describes the bound nodes.
type LeaseSlice struct {
	Nodes []string `json:"nodes"`
	Role  string   `json:"role"`
}

// LeaseInterval timestamps the lease.
type LeaseInterval struct {
	Start Time  `json:"start"`
	End   *Time `json:"end,omitempty"`
}

// LeaseStatus captures closure state.
type LeaseStatus struct {
	Closed bool  `json:"closed"`
	Ended  *Time `json:"ended,omitempty"`
}

// LeaseList lists leases.
// +kubebuilder:object:root=true
type LeaseList struct {
	TypeMeta `json:",inline"`
	ListMeta `json:"metadata,omitempty"`
	Items    []Lease `json:"items"`
}

// ValidateCreate ensures lease is consistent.
func (l *Lease) ValidateCreate() error {
	return l.validate()
}

// ValidateUpdate enforces immutability except status.
func (l *Lease) ValidateUpdate(old RuntimeObject) error {
	prev, ok := old.(*Lease)
	if !ok {
		return fmt.Errorf("expected Lease in update")
	}
	if !reflect.DeepEqual(l.Spec, prev.Spec) {
		return fmt.Errorf("spec is immutable; close and recreate")
	}
	return l.validate()
}

// ValidateDelete always allows deletion.
func (l *Lease) ValidateDelete() error {
	return nil
}

func (l *Lease) validate() error {
	if l.Spec.Owner == "" {
		return fmt.Errorf("spec.owner is required")
	}
	if l.Spec.RunRef.Name == "" {
		return fmt.Errorf("spec.runRef.name is required")
	}
	if l.Spec.PaidByEnvelope == "" {
		return fmt.Errorf("spec.paidByEnvelope is required")
	}
	if l.Spec.Interval.Start.IsZero() {
		return fmt.Errorf("spec.interval.start is required")
	}
	if len(l.Spec.Slice.Nodes) == 0 {
		return fmt.Errorf("spec.slice.nodes must not be empty")
	}
	if l.Spec.Slice.Role == "" {
		return fmt.Errorf("spec.slice.role is required")
	}
	return nil
}

// DeepCopyInto deep copies Lease.
func (in *Lease) DeepCopyInto(out *Lease) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = *in.Spec.DeepCopy()
	out.Status = *in.Status.DeepCopy()
}

// DeepCopy deep copies Lease.
func (in *Lease) DeepCopy() *Lease {
	if in == nil {
		return nil
	}
	out := new(Lease)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *Lease) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto deep copies LeaseList.
func (in *LeaseList) DeepCopyInto(out *LeaseList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Lease, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy deep copies list.
func (in *LeaseList) DeepCopy() *LeaseList {
	if in == nil {
		return nil
	}
	out := new(LeaseList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *LeaseList) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopy copies LeaseSpec.
func (in *LeaseSpec) DeepCopy() *LeaseSpec {
	if in == nil {
		return nil
	}
	out := new(LeaseSpec)
	*out = *in
	if in.CompPath != nil {
		out.CompPath = append([]string{}, in.CompPath...)
	}
	out.Slice = *in.Slice.DeepCopy()
	out.Interval = *in.Interval.DeepCopy()
	return out
}

// DeepCopy copies LeaseSlice.
func (in *LeaseSlice) DeepCopy() *LeaseSlice {
	if in == nil {
		return nil
	}
	out := new(LeaseSlice)
	*out = *in
	if in.Nodes != nil {
		out.Nodes = append([]string{}, in.Nodes...)
	}
	return out
}

// DeepCopy copies interval.
func (in *LeaseInterval) DeepCopy() *LeaseInterval {
	if in == nil {
		return nil
	}
	out := new(LeaseInterval)
	*out = *in
	if in.End != nil {
		value := in.End.DeepCopy()
		out.End = &value
	}
	return out
}

// DeepCopy copies status.
func (in *LeaseStatus) DeepCopy() *LeaseStatus {
	if in == nil {
		return nil
	}
	out := new(LeaseStatus)
	*out = *in
	if in.Ended != nil {
		value := in.Ended.DeepCopy()
		out.Ended = &value
	}
	return out
}

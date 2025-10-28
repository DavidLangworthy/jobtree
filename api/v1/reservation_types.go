package v1

import (
	"fmt"
	"reflect"
)

// Reservation plans future consumption.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.runRef.name`
// +kubebuilder:printcolumn:name="Earliest",type=string,JSONPath=`.spec.earliestStart`
type Reservation struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata,omitempty"`

	Spec   ReservationSpec   `json:"spec,omitempty"`
	Status ReservationStatus `json:"status,omitempty"`
}

// ReservationSpec captures the immutable plan.
type ReservationSpec struct {
	RunRef         RunReference  `json:"runRef"`
	IntendedSlice  IntendedSlice `json:"intendedSlice"`
	PayingEnvelope string        `json:"payingEnvelope"`
	EarliestStart  Time          `json:"earliestStart"`
}

// IntendedSlice defines the target topology.
type IntendedSlice struct {
	Domain map[string]string `json:"domain,omitempty"`
	Nodes  []string          `json:"nodes,omitempty"`
}

// RunReference links to the Run.
type RunReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// ReservationStatus reports lifecycle transitions.
type ReservationStatus struct {
	State       string `json:"state,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ActivatedAt *Time  `json:"activatedAt,omitempty"`
	ReleasedAt  *Time  `json:"releasedAt,omitempty"`
	CanceledAt  *Time  `json:"canceledAt,omitempty"`
}

// ReservationList lists reservations.
// +kubebuilder:object:root=true
type ReservationList struct {
	TypeMeta `json:",inline"`
	ListMeta `json:"metadata,omitempty"`
	Items    []Reservation `json:"items"`
}

// ValidateCreate ensures Reservation is well formed.
func (r *Reservation) ValidateCreate() error {
	return r.validate()
}

// ValidateUpdate ensures Reservation update still valid (immutable spec).
func (r *Reservation) ValidateUpdate(old RuntimeObject) error {
	oldReservation, ok := old.(*Reservation)
	if !ok {
		return fmt.Errorf("expected Reservation in update")
	}
	if !reflect.DeepEqual(r.Spec, oldReservation.Spec) {
		return fmt.Errorf("spec is immutable; cancel and recreate for changes")
	}
	return r.validate()
}

// ValidateDelete allows deletion.
func (r *Reservation) ValidateDelete() error {
	return nil
}

func (r *Reservation) validate() error {
	if r.Spec.RunRef.Name == "" {
		return fmt.Errorf("spec.runRef.name is required")
	}
	if r.Spec.PayingEnvelope == "" {
		return fmt.Errorf("spec.payingEnvelope is required")
	}
	if r.Spec.EarliestStart.IsZero() {
		return fmt.Errorf("spec.earliestStart is required")
	}
	if len(r.Spec.IntendedSlice.Nodes) == 0 && len(r.Spec.IntendedSlice.Domain) == 0 {
		return fmt.Errorf("spec.intendedSlice must set nodes or domain")
	}
	return nil
}

// DeepCopyInto deep copies Reservation.
func (in *Reservation) DeepCopyInto(out *Reservation) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = *in.Spec.DeepCopy()
	out.Status = *in.Status.DeepCopy()
}

// DeepCopy deep copies Reservation.
func (in *Reservation) DeepCopy() *Reservation {
	if in == nil {
		return nil
	}
	out := new(Reservation)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *Reservation) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto deep copies ReservationList.
func (in *ReservationList) DeepCopyInto(out *ReservationList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Reservation, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy deep copies list.
func (in *ReservationList) DeepCopy() *ReservationList {
	if in == nil {
		return nil
	}
	out := new(ReservationList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject deep copies runtime object.
func (in *ReservationList) DeepCopyObject() RuntimeObject {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopy copies ReservationSpec.
func (in *ReservationSpec) DeepCopy() *ReservationSpec {
	if in == nil {
		return nil
	}
	out := new(ReservationSpec)
	*out = *in
	if in.IntendedSlice.Domain != nil {
		out.IntendedSlice.Domain = make(map[string]string, len(in.IntendedSlice.Domain))
		for k, v := range in.IntendedSlice.Domain {
			out.IntendedSlice.Domain[k] = v
		}
	}
	if in.IntendedSlice.Nodes != nil {
		out.IntendedSlice.Nodes = append([]string{}, in.IntendedSlice.Nodes...)
	}
	return out
}

// DeepCopy copies ReservationStatus.
func (in *ReservationStatus) DeepCopy() *ReservationStatus {
	if in == nil {
		return nil
	}
	out := new(ReservationStatus)
	*out = *in
	if in.ActivatedAt != nil {
		value := in.ActivatedAt.DeepCopy()
		out.ActivatedAt = &value
	}
	if in.ReleasedAt != nil {
		value := in.ReleasedAt.DeepCopy()
		out.ReleasedAt = &value
	}
	if in.CanceledAt != nil {
		value := in.CanceledAt.DeepCopy()
		out.CanceledAt = &value
	}
	return out
}

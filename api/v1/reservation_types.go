package v1

import (
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reservation plans future consumption.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.runRef.name`
// +kubebuilder:printcolumn:name="Earliest",type=string,JSONPath=`.spec.earliestStart`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="spec is immutable; cancel and recreate for changes"
type Reservation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReservationSpec   `json:"spec,omitempty"`
	Status ReservationStatus `json:"status,omitempty"`
}

// ReservationSpec captures the immutable plan.
//
// R14: a Reservation's spec is immutable for the same reason a lease's is — it is a
// promise the resolver has already planned around — and that immutability now holds
// at the apiserver, not only in the webhook. The intendedSlice rule is the webhook's
// own cross-field check moved down to where it cannot be bypassed.
//
// +kubebuilder:validation:XValidation:rule="has(self.intendedSlice.nodes) || has(self.intendedSlice.domain)",message="spec.intendedSlice must set nodes or domain"
type ReservationSpec struct {
	RunRef        RunReference  `json:"runRef"`
	IntendedSlice IntendedSlice `json:"intendedSlice"`
	// +kubebuilder:validation:MinLength=1
	PayingEnvelope string      `json:"payingEnvelope"`
	EarliestStart  metav1.Time `json:"earliestStart"`
}

// IntendedSlice defines the target topology.
type IntendedSlice struct {
	Domain map[string]string `json:"domain,omitempty"`
	Nodes  []string          `json:"nodes,omitempty"`
}

// RunReference links to the Run.
type RunReference struct {
	// +kubebuilder:validation:MinLength=1
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// ReservationStatus reports lifecycle transitions.
type ReservationStatus struct {
	// Conditions reports Forecast/Activated (R11), derived by
	// SetReservationConditions from the fields below.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions       []metav1.Condition   `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	State            string               `json:"state,omitempty"`
	Reason           string               `json:"reason,omitempty"`
	ActivatedAt      *metav1.Time         `json:"activatedAt,omitempty"`
	ReleasedAt       *metav1.Time         `json:"releasedAt,omitempty"`
	CanceledAt       *metav1.Time         `json:"canceledAt,omitempty"`
	CountdownSeconds *int64               `json:"countdownSeconds,omitempty"`
	Forecast         *ReservationForecast `json:"forecast,omitempty"`
}

// ReservationForecast communicates expected activation details.
type ReservationForecast struct {
	DeficitGPUs int32             `json:"deficitGPUs,omitempty"`
	Scope       map[string]string `json:"scope,omitempty"`
	Remedies    []string          `json:"remedies,omitempty"`
	Confidence  string            `json:"confidence,omitempty"`
}

// ReservationList lists reservations.
// +kubebuilder:object:root=true
type ReservationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Reservation `json:"items"`
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

package v1

import (
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Lease records immutable consumption facts.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.runRef.name`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.slice.role`
// +kubebuilder:printcolumn:name="Start",type=string,JSONPath=`.spec.interval.start`
// An OPEN lease charges a budget and holds GPUs; make that the first thing
// `kubectl get` shows (R11).
// +kubebuilder:printcolumn:name="Closed",type=boolean,JSONPath=`.status.closed`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.closureReason`
type Lease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LeaseSpec   `json:"spec,omitempty"`
	Status LeaseStatus `json:"status,omitempty"`
}

// LeaseSpec is immutable after creation.
type LeaseSpec struct {
	Owner    string        `json:"owner"`
	RunRef   RunReference  `json:"runRef"`
	CompPath []string      `json:"compPath,omitempty"`
	Slice    LeaseSlice    `json:"slice"`
	Interval LeaseInterval `json:"interval"`
	// PaidByBudgetNamespace scopes PaidByBudget: Budgets are namespaced, so the
	// budget name alone does not identify one — two tenants can each own a Budget
	// of the same name in their own namespace. The funding index keys on all three
	// (namespace, budget, envelope); without the namespace their envelopes collide
	// and one tenant charges the other (Codex #1 / task #62). Empty on leases
	// written before the field existed; the funding fold treats an empty namespace
	// as its own key, so legacy leases keep matching legacy (empty-namespace)
	// index entries and are not silently re-pointed.
	PaidByBudgetNamespace string `json:"paidByBudgetNamespace,omitempty"`
	// PaidByBudget scopes PaidByEnvelope: envelope names are only unique
	// within one budget, and one owner can hold several budgets. Empty on
	// leases written before the field existed; those fall back to
	// owner+envelope attribution.
	PaidByBudget   string `json:"paidByBudget,omitempty"`
	PaidByEnvelope string `json:"paidByEnvelope"`
	Reason         string `json:"reason"`
}

// LeaseSlice describes the bound nodes.
type LeaseSlice struct {
	Nodes []string `json:"nodes"`
	Role  string   `json:"role"`
}

// LeaseInterval timestamps the lease.
type LeaseInterval struct {
	Start metav1.Time  `json:"start"`
	End   *metav1.Time `json:"end,omitempty"`
}

// LeaseStatus captures closure state.
type LeaseStatus struct {
	// Conditions mirrors Closed/ClosureReason as Active/Closed conditions (R11)
	// so an open lease — the object that charges a budget and holds GPUs — is
	// selectable and waitable. SetLeaseConditions DERIVES these from the two
	// fields below; it never writes them, because controllers.CloseLease is the
	// sole closer and hack/antifake enforces it.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions    []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	Closed        bool               `json:"closed"`
	Ended         *metav1.Time       `json:"ended,omitempty"`
	ClosureReason string             `json:"closureReason,omitempty"`
}

// LeaseList lists leases.
// +kubebuilder:object:root=true
type LeaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Lease `json:"items"`
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

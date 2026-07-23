package v1

import (
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GPULease records immutable consumption facts.
//
// The kind is GPULease, not Lease. `rq.davidlangworthy.io`'s Lease collided with
// the core coordination.k8s.io Lease that every leader election in the cluster
// uses, so `kubectl get leases` was ambiguous and RBAC written against `leases`
// could grant the wrong resource entirely (R13).
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=gpuleases,shortName=gl
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.runRef.name`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.slice.role`
// +kubebuilder:printcolumn:name="Start",type=string,JSONPath=`.spec.interval.start`
// An OPEN lease charges a budget and holds GPUs; make that the first thing
// `kubectl get` shows (R11).
// +kubebuilder:printcolumn:name="Closed",type=boolean,JSONPath=`.status.closed`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.closureReason`
//
// R14: spec immutability is an APISERVER rule, not a webhook rule. A lease is a
// funding fact — who paid, for which slots, from when — and the whole ledger is a
// fold over those facts. If the validating webhook is down (it is
// failurePolicy=Fail, so "down" means either everything blocks or someone flipped
// it to Ignore), a webhook-only immutability check evaporates exactly when the
// cluster is least healthy, and a rewritten payer silently re-attributes spend
// that already happened. CEL runs in the apiserver, so it holds regardless.
//
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="spec is immutable; close this lease and mint a new one"
type GPULease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPULeaseSpec   `json:"spec,omitempty"`
	Status GPULeaseStatus `json:"status,omitempty"`
}

// GPULeaseSpec is immutable after creation.
type GPULeaseSpec struct {
	// +kubebuilder:validation:MinLength=1
	Owner    string           `json:"owner"`
	RunRef   RunReference     `json:"runRef"`
	CompPath []string         `json:"compPath,omitempty"`
	Slice    GPULeaseSlice    `json:"slice"`
	Interval GPULeaseInterval `json:"interval"`
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
	PaidByBudget string `json:"paidByBudget,omitempty"`
	// An open lease with no payer envelope charges nobody while holding real
	// GPUs, which is the shape of every funding leak on this board.
	// +kubebuilder:validation:MinLength=1
	PaidByEnvelope string `json:"paidByEnvelope"`
	Reason         string `json:"reason"`
}

// GPULeaseSlice describes the bound nodes.
type GPULeaseSlice struct {
	// Slots, as node#ordinal. A lease holding no slot is a charge for nothing.
	// +kubebuilder:validation:MinItems=1
	Nodes []string `json:"nodes"`
	// +kubebuilder:validation:Enum=Active;Spare
	Role string `json:"role"`
}

// GPULeaseInterval timestamps the lease.
type GPULeaseInterval struct {
	Start metav1.Time  `json:"start"`
	End   *metav1.Time `json:"end,omitempty"`
}

// GPULeaseStatus captures closure state.
//
// R14: closure is MONOTONE at the apiserver. Reopening a closed lease would make a
// settled interval start billing again from its original start instant, and
// pkg/invariant's INV-CLOSED-MONOTONE already treats that as an illegal state — this
// makes the API refuse to represent it at all, webhook up or down. A transition rule
// only runs when there is an oldSelf, so a create is unaffected.
//
// +kubebuilder:validation:XValidation:rule="!oldSelf.closed || self.closed",message="closed is monotone: a closed lease is a settled fact and cannot be reopened"
type GPULeaseStatus struct {
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

// GPULeaseList lists GPU leases.
// +kubebuilder:object:root=true
type GPULeaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPULease `json:"items"`
}

// ValidateCreate ensures the lease is consistent.
func (l *GPULease) ValidateCreate() error {
	return l.validate()
}

// ValidateUpdate enforces immutability except status.
func (l *GPULease) ValidateUpdate(old RuntimeObject) error {
	prev, ok := old.(*GPULease)
	if !ok {
		return fmt.Errorf("expected GPULease in update")
	}
	if !reflect.DeepEqual(l.Spec, prev.Spec) {
		return fmt.Errorf("spec is immutable; close and recreate")
	}
	return l.validate()
}

// ValidateDelete always allows deletion.
func (l *GPULease) ValidateDelete() error {
	return nil
}

func (l *GPULease) validate() error {
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

package v1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GrantSpec is what a principal GIVES AWAY (DESIGN-v5 §2).
//
// Grant is a separate resource from Budget, and that split is the whole security
// model: RBAC is per-resource, not per-field. As a field on Budget, "what I
// delegate" would need the same `update` verb as "how much I hold", so anyone who
// could sub-divide could also enlarge themselves. Splitting it means a lead holds
// write on grants and only READ on budgets (see the grantor ClusterRole in
// deploy/helm/gpu-fleet/templates/rbac.yaml).
//
// A Grant lives in the GRANTOR's namespace. There is deliberately no grantor
// field: INV-NO-SELF-GRANT derives the grantor from `metadata.namespace`'s
// immutable UID, never from anything writable, so "can you write here" IS the
// authority check and forging a grantor is not expressible.
type GrantSpec struct {
	// GranteeOwner names the principal receiving the authority.
	// +kubebuilder:validation:MinLength=1
	GranteeOwner string `json:"granteeOwner"`
	// GranteeNamespace is the grantee's namespace NAME, resolved to a UID by the
	// producer. The name is the human-writable half; the UID recorded in the
	// compiled snapshot is what identity actually keys on, because a namespace
	// deleted and recreated under the same name is a different principal.
	// +kubebuilder:validation:MinLength=1
	GranteeNamespace string `json:"granteeNamespace"`
	// Caps bound the delegated authority per flavour. A flavour absent here is
	// not granted at all — absent inbound authority means ZERO (§2b), never a
	// fallback to the grantee's own Budget and never an implicit root.
	// +kubebuilder:validation:MinItems=1
	Caps []GrantCap `json:"caps"`
	// Start and End are required, exactly as an envelope's are
	// (INV-WINDOW-REQUIRED): a delegation that never expires is a delegation
	// nobody has to think about again.
	// +kubebuilder:validation:Required
	Start *metav1.Time `json:"start"`
	// +kubebuilder:validation:Required
	End *metav1.Time `json:"end"`
}

// GrantCap is one flavour's delegated concurrency ceiling.
type GrantCap struct {
	// +kubebuilder:validation:MinLength=1
	Flavor string `json:"flavor"`
	// +kubebuilder:validation:Minimum=0
	MaxConcurrency int32 `json:"maxConcurrency"`
}

// GrantStatus reports how the producer compiled this Grant.
type GrantStatus struct {
	// Conditions carry Accepted / Quarantined. Quarantine attaches to a WRITE,
	// never to a principal (§4), so it is reported here on the object that caused
	// it rather than on the grantee.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// AcceptedRevision is the resourceVersion of the last revision that compiled
	// in. Quarantine is REVISION-GRANULAR: when an update to an accepted Grant is
	// invalid, the accepted revision stays authoritative and only the candidate is
	// credit-free (§4.3). Without this the two halves of that rule contradict
	// each other, which is the v4 defect it fixes.
	AcceptedRevision string `json:"acceptedRevision,omitempty"`
	// SnapshotVersion is the snapshot that last incorporated this Grant.
	SnapshotVersion string `json:"snapshotVersion,omitempty"`
}

// Grant delegates a slice of a principal's allocation to another principal.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=grants,scope=Namespaced
// +kubebuilder:validation:XValidation:rule="self.spec.end > self.spec.start",message="end must be after start"
// +kubebuilder:printcolumn:name="Grantee",type=string,JSONPath=`.spec.granteeOwner`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.granteeNamespace`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
type Grant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GrantSpec   `json:"spec,omitempty"`
	Status GrantStatus `json:"status,omitempty"`
}

// GrantList contains a list of Grants.
// +kubebuilder:object:root=true
type GrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Grant `json:"items"`
}

// Validate checks the field-level rules the producer relies on. Structural
// rules that need the whole graph (acyclicity, single inbound authority,
// endpoint resolution) belong to the producer, not here: they are not properties
// of one object.
func (g *GrantSpec) Validate() error {
	if g.GranteeOwner == "" {
		return fmt.Errorf("granteeOwner is required")
	}
	if g.GranteeNamespace == "" {
		return fmt.Errorf("granteeNamespace is required")
	}
	if len(g.Caps) == 0 {
		return fmt.Errorf("caps must grant at least one flavour: an empty grant delegates nothing, which is better expressed by deleting it")
	}
	seen := map[string]struct{}{}
	for i := range g.Caps {
		c := &g.Caps[i]
		if c.Flavor == "" {
			return fmt.Errorf("caps[%d]: flavor is required", i)
		}
		if c.MaxConcurrency < 0 {
			return fmt.Errorf("caps[%d]: maxConcurrency must be non-negative", i)
		}
		if _, dup := seen[c.Flavor]; dup {
			return fmt.Errorf("caps names flavor %q more than once", c.Flavor)
		}
		seen[c.Flavor] = struct{}{}
	}
	if g.Start == nil || g.End == nil {
		return fmt.Errorf("start and end are both required: a grant that never expires is authority nobody revisits (INV-WINDOW-REQUIRED)")
	}
	if !g.End.Time.After(g.Start.Time) {
		return fmt.Errorf("end must be after start")
	}
	return nil
}

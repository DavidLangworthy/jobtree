package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// QuotaSnapshotSchemaVersion is the document's own format version, bumped when
// the shape below changes incompatibly. It is NOT the snapshotVersion, which
// identifies one compiled instance.
const QuotaSnapshotSchemaVersion = "v1"

// QuotaSnapshotSpec is the compiled, versioned identity document (DESIGN-v5 §3).
//
// This is what replaces "the scheduler derives owners by scanning". Identity
// stops being a function every reader recomputes from Budgets and becomes a
// document one in-tree producer publishes, so every reader agrees by
// construction rather than by everyone implementing the same walk correctly.
//
// One document. Sharding is not built (Ruling 18); revisit above ~2,000
// principals.
type QuotaSnapshotSpec struct {
	// SchemaVersion is the document format (QuotaSnapshotSchemaVersion).
	SchemaVersion string `json:"schemaVersion"`
	// SnapshotVersion identifies this compiled instance. Monotone
	// (INV-SNAP-MONOTONE) and immutable once published (INV-SNAP-IMMUTABLE).
	SnapshotVersion string `json:"snapshotVersion"`
	// EffectiveFrom is the instant this snapshot's classifications take effect.
	//
	// RETAINED, WITH ITS REASON CORRECTED (§3). v4 justified this by accrual
	// anchoring, and Ruling 10 deleted the accrual. Its actual consumer is the
	// METER's interval-split boundary: when a snapshot changes a principal's
	// class, the meter seals the old interval at that instant, and two meters
	// using wall-clock observation time would disagree about where the seam is.
	// Persisted effective time is required; observation time is insufficient.
	// INV-SNAP-IMMUTABLE survives for the same reason.
	EffectiveFrom metav1.Time `json:"effectiveFrom"`
	// ContentHash covers the compiled content (not the version or timestamp), so
	// a recompile that changes nothing is recognisable as a no-op.
	ContentHash string `json:"contentHash"`
	// Roots are the principals with no inbound authority that are nonetheless
	// authoritative — the top of each tree. A principal absent from here with no
	// inbound Grant has ZERO, never an implicit root (§2b).
	Roots []string `json:"roots,omitempty"`
	// Principals is the compiled graph.
	Principals []SnapshotPrincipal `json:"principals,omitempty"`
}

// Principal statuses. Quarantine attaches to a WRITE, never to a principal (§4),
// so a principal is Quarantined only in the sense that the write naming it did
// not compile in — its incumbent authority is untouched.
const (
	PrincipalAccepted    = "Accepted"
	PrincipalQuarantined = "Quarantined"
)

// SnapshotPrincipal is one compiled principal.
type SnapshotPrincipal struct {
	// Owner is the principal's identity within the tree.
	Owner string `json:"owner"`
	// Status is Accepted or Quarantined.
	Status string `json:"status"`
	// QuarantineTrigger names the object whose write was quarantined and why —
	// "and since when" is what `kubectl describe run` needs to answer *"why is my
	// job Unfunded?"* (§10).
	QuarantineTrigger string `json:"quarantineTrigger,omitempty"`
	// BoundNamespace binds this principal to exactly one namespace, by NAME for
	// humans and by UID for identity. The UID is the half that matters: a
	// namespace deleted and recreated under the same name is a DIFFERENT
	// principal, and only the UID can say so.
	BoundNamespace NamespaceBinding `json:"boundNamespace"`
	// Children are the principals this one grants to, by owner.
	Children []string `json:"children,omitempty"`
	// InboundGrant is the per-edge provenance: which Grant object authorised this
	// principal, at which revision. Absent means no inbound authority, which
	// means ZERO — never a fallback to this principal's own Budget.
	InboundGrant *GrantProvenance `json:"inboundGrant,omitempty"`
	// Envelopes are the principal's effective envelopes: its own Budget's
	// envelopes CLAMPED on every dimension by the grant that authorised them.
	Envelopes []SnapshotEnvelope `json:"envelopes,omitempty"`
}

// NamespaceBinding identifies a principal's namespace by name and UID.
type NamespaceBinding struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// GrantProvenance records which Grant revision authorised an edge.
type GrantProvenance struct {
	// Namespace and Name locate the Grant object; the grantor is the namespace.
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Revision is the resourceVersion that compiled in. Quarantine is
	// revision-granular, so this says WHICH revision is authoritative — an
	// invalid update leaves this pointing at the accepted one (§4.3).
	Revision string `json:"revision,omitempty"`
	// Grantor is the granting principal's owner, derived from the Grant's
	// namespace UID and never from a writable field (INV-NO-SELF-GRANT).
	Grantor string `json:"grantor,omitempty"`
}

// SnapshotEnvelope is one effective envelope after clamping.
//
// EVERYTHING CLAMPS — NO DIMENSION REJECTS (§2a). v4 clamped concurrency and
// left time as a rejection invariant, which recreated the self-defeating-cut
// defect on the time axis: with grant and envelope both [0,100), a grantor
// accelerating the end to 50 made every outcome wrong. So:
//
//	effectiveConcurrency = min(envelope.concurrency, grant.cap)
//	effectiveWindow      = envelope.window ∩ grant.window
//
// Authored overhang is LEGAL, VISIBLE over-allocation — not invalid state.
type SnapshotEnvelope struct {
	Name   string `json:"name"`
	Flavor string `json:"flavor"`
	// Concurrency is the EFFECTIVE concurrency after clamping.
	Concurrency int32 `json:"concurrency"`
	// Start and End are the EFFECTIVE window: the intersection of the envelope's
	// window and its authorising grant's.
	Start metav1.Time `json:"start"`
	End   metav1.Time `json:"end"`
	// OverAllocatedBy is how much authored concurrency exceeded the grant, per
	// flavour. ABSENT, NOT ZERO, when there is no over-allocation (Ruling 3 /
	// §9) — a zero would be indistinguishable from "conservation is not built
	// yet", and the difference is exactly what an operator is asking about.
	OverAllocatedBy *int32 `json:"overAllocatedBy,omitempty"`
	// OverAllocatedUntil is the TEMPORAL diagnostic §2a requires. Per-flavour
	// overAllocatedBy cannot say "your envelope extends 12 days past its
	// authority", and that is a different repair from lowering a number.
	OverAllocatedUntil *metav1.Time `json:"overAllocatedUntil,omitempty"`
}

// QuotaSnapshotStatus reports publication.
type QuotaSnapshotStatus struct {
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// PrincipalCount and QuarantinedCount are the at-a-glance numbers. A nonzero
	// quarantine count is an authority loss somebody should look at — §4's whole
	// point is that it must never be silent.
	PrincipalCount   int32 `json:"principalCount,omitempty"`
	QuarantinedCount int32 `json:"quarantinedCount,omitempty"`
}

// QuotaSnapshot is the published identity document.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=quotasnapshots,scope=Cluster,shortName=qsnap
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.snapshotVersion`
// +kubebuilder:printcolumn:name="Effective",type=string,JSONPath=`.spec.effectiveFrom`
// +kubebuilder:printcolumn:name="Principals",type=integer,JSONPath=`.status.principalCount`
// +kubebuilder:printcolumn:name="Quarantined",type=integer,JSONPath=`.status.quarantinedCount`
type QuotaSnapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QuotaSnapshotSpec   `json:"spec,omitempty"`
	Status QuotaSnapshotStatus `json:"status,omitempty"`
}

// QuotaSnapshotList contains a list of QuotaSnapshots.
// +kubebuilder:object:root=true
type QuotaSnapshotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaSnapshot `json:"items"`
}

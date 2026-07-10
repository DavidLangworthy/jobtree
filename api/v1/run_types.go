package v1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
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
	Owner     string       `json:"owner"`
	Resources RunResources `json:"resources"`
	// Roles is the researcher's real workload: one homogeneous pod pool per
	// role, materialized directly as a cohort of pods that the jobtree
	// scheduler plugin binds and funds. JobSet was evaluated as the substrate
	// and rejected — it cannot express the spare swap or delta-funded elastic
	// width (docs/project/remediation/R9-jobset-amendment.md); we keep its
	// shape as a reference contract and own the pods.
	//
	// v1 admits exactly one role; the field is a list so heterogeneous
	// multi-role Runs (RL gang-of-gangs: trainer/sampler/grader) land later as
	// a purely additive, non-breaking change (borrow-vs-build.md §2.2).
	//
	// Roles is optional: a Run with no role still materializes, but with a
	// default terminating container rather than the researcher's workload. That
	// legacy path exists for the engine's own tests and for Runs written before
	// roles landed; it is not a workload surface anyone should target.
	Roles     []RunRole        `json:"roles,omitempty"`
	Locality  *RunLocality     `json:"locality,omitempty"`
	Runtime   *RunRuntime      `json:"runtime,omitempty"`
	Malleable *RunMalleability `json:"malleable,omitempty"`
	Funding   *RunFunding      `json:"funding,omitempty"`
	Spares    *int32           `json:"sparesPerGroup,omitempty"`
	Follow    *RunFollow       `json:"follow,omitempty"`
}

// GPUTargetContainerName is the convention for the container that receives the
// injected nvidia.com/gpu request/limit — and, once R9 phase 9A-2 lands, the
// rendezvous env. A role's template should name its workload container this; if
// none matches, the first container is the target. Kept here (not in a
// controller package) so the webhook validation and the pod-emit path agree on
// one definition.
const GPUTargetContainerName = "workload"

// RunRole is one homogeneous pool of pods within a Run — the unit jobtree
// materializes as a cohort of pods it owns. (JobSet calls the same shape a
// ReplicatedJob; we keep the shape as a reference contract and not as a
// dependency — see controllers/kube.buildPod.) It carries the per-role workload
// template plus the width/topology/spare knobs that were previously spread
// across RunSpec, so a future multi-role Run can size each role independently.
type RunRole struct {
	// Name identifies the role (e.g. "trainer"). It becomes the gang-role label
	// value and the pod-name prefix, so it must be a non-empty DNS label.
	Name string `json:"name"`

	// Template is the researcher's workload pod. jobtree deep-copies it per
	// materialized slice and overlays only the scheduling-owned fields
	// (schedulerName; nodeName is never set — the plugin binds it; the
	// nvidia.com/gpu limit; gang labels; restartPolicy=Never). Everything else
	// — image, command, env, volumes, resources — is the researcher's and is
	// preserved verbatim.
	//
	// Rendezvous env (MASTER_ADDR/MASTER_PORT/WORLD_SIZE/NNODES/NODE_RANK) is
	// NOT injected yet: it lands with R9 phase 9A-2, and until then a role with
	// width > 1 cannot form a process group. Saying otherwise here is what R10
	// was raised to fix.
	//
	// The field is marked PreserveUnknownFields so controller-gen does NOT
	// inline the (hundreds-of-KB) PodTemplateSpec OpenAPI schema into the CRD —
	// that would blow the 262144-byte last-applied-configuration annotation
	// limit under `kubectl apply`. The template is validated in the webhook
	// (>=1 container, non-empty image on the GPU-target container, no
	// jobtree-owned fields) instead of by the apiserver's structural schema.
	//
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Template corev1.PodTemplateSpec `json:"template"`

	// Width is the number of pods in this role's gang: all of them run, or none
	// does. Must be positive. Width*GPUsPerPod must equal the Run's
	// Resources.TotalGPUs in v1 (single role).
	Width int32 `json:"width"`

	// GPUsPerPod is the nvidia.com/gpu request (== limit, extended resources
	// are non-overcommit) injected on the GPU-target container of each pod.
	// Must be positive; the zero-GPU CPU-only role path is a later addition.
	GPUsPerPod int32 `json:"gpusPerPod"`

	// GroupGPUs optionally overrides spec.locality.groupGPUs for this role: the
	// number of GPUs packed into one fabric domain. Positive when set.
	GroupGPUs *int32 `json:"groupGPUs,omitempty"`

	// Spares optionally overrides spec.sparesPerGroup for this role: hot spares
	// held per group for fast node-failure swap. Non-negative when set.
	Spares *int32 `json:"spares,omitempty"`
}

// GPUTargetContainerIndex returns the index of the container that receives the
// injected nvidia.com/gpu request: the one named GPUTargetContainerName by
// convention, otherwise the first container. Returns -1 when the template has
// no containers. The webhook and the pod-emit path both use this so a template
// can never silently produce a zero-GPU pod.
func (r *RunRole) GPUTargetContainerIndex() int {
	containers := r.Template.Spec.Containers
	if len(containers) == 0 {
		return -1
	}
	for i := range containers {
		if containers[i].Name == GPUTargetContainerName {
			return i
		}
	}
	return 0
}

// Upstream-failure policies for a followed run.
const (
	OnUpstreamFailureWait = "wait"
	OnUpstreamFailureFail = "fail"
)

// RunFollow makes a run wait for other runs in the same namespace to complete
// before it is admitted — a "job forest" of runs joined by follow edges. All
// runs in After must reach Completed. If one fails (or is deleted),
// onUpstreamFailure decides: "wait" (default) keeps this run Waiting for a
// grace period so the researcher can fix and resubmit the failed stage, then
// fails it; "fail" fails this run immediately.
type RunFollow struct {
	After []string `json:"after"`
	// +kubebuilder:validation:Enum="";wait;fail
	OnUpstreamFailure    string           `json:"onUpstreamFailure,omitempty"`
	UpstreamFailureGrace *metav1.Duration `json:"upstreamFailureGrace,omitempty"`
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
	ETA                *RunETA           `json:"eta,omitempty"`
	// FollowDeadline is set while the run waits on a failed upstream under the
	// "wait" policy: if the upstream is not resolved by then, the run fails.
	FollowDeadline *metav1.Time `json:"followDeadline,omitempty"`
	// CheckpointDeadline is set when a node fails with no spare to swap onto
	// and spec.runtime.checkpoint is a positive duration: instead of failing
	// immediately, the run is parked Pending and given until this deadline
	// to re-admit (bind directly or via a reservation) before it is failed
	// terminally. A zero/unset Checkpoint keeps the old behavior of failing
	// immediately on an uncovered node failure.
	CheckpointDeadline *metav1.Time `json:"checkpointDeadline,omitempty"`
}

// RunETA is an optional, best-effort estimate of when the run will finish. It
// is observability only — nothing in scheduling reads it and there is no
// penalty for omitting it. The workload reports it (a pod annotation the
// controller mirrors, source "job"); the CLI can set it directly (source
// "controller").
type RunETA struct {
	EstimatedCompletion metav1.Time `json:"estimatedCompletion"`
	ReportedAt          metav1.Time `json:"reportedAt,omitempty"`
	Source              string      `json:"source,omitempty"`
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
	if r.Spec.Follow != nil {
		if err := r.Spec.Follow.Validate(r.Name); err != nil {
			return err
		}
	}
	if err := r.Spec.validateRoles(); err != nil {
		return err
	}
	return nil
}

// validateRoles enforces the v1 workload contract. Roles is optional while the
// legacy pause-pod path still exists; when present, v1 admits exactly one role
// (multi-role RL is a later additive fast-follow) and fully validates it.
func (s *RunSpec) validateRoles() error {
	if len(s.Roles) == 0 {
		return nil
	}
	if len(s.Roles) > 1 {
		return fmt.Errorf("spec.roles: v1 supports exactly one role; multi-role runs are a later additive feature")
	}
	role := &s.Roles[0]
	if role.Name == "" {
		return fmt.Errorf("spec.roles[0].name is required")
	}
	if role.Width <= 0 {
		return fmt.Errorf("spec.roles[0].width must be positive")
	}
	if role.GPUsPerPod <= 0 {
		return fmt.Errorf("spec.roles[0].gpusPerPod must be positive")
	}
	if role.Width*role.GPUsPerPod != s.Resources.TotalGPUs {
		return fmt.Errorf("spec.roles[0]: width*gpusPerPod (%d) must equal resources.totalGPUs (%d)", role.Width*role.GPUsPerPod, s.Resources.TotalGPUs)
	}
	if role.GroupGPUs != nil && *role.GroupGPUs <= 0 {
		return fmt.Errorf("spec.roles[0].groupGPUs must be positive when set")
	}
	if role.Spares != nil && *role.Spares < 0 {
		return fmt.Errorf("spec.roles[0].spares must be >= 0 when set")
	}
	return role.validateTemplate()
}

// validateTemplate checks the workload pod template. Because the template is
// stored with PreserveUnknownFields (no structural schema in the CRD), these
// checks are the only guard against a malformed or reserved-field-setting
// template: at least one container, a non-empty image on the GPU-target
// container, and none of the jobtree-owned pod fields (nodeName, schedulerName,
// restartPolicy) set by the researcher.
// ReservedRendezvousEnvNames are container env vars jobtree injects for
// distributed-training rendezvous (R9 9A-2). A researcher must not set them: the
// injected value is authoritative, and a researcher-set one would only be silently
// overridden, so reject it at submission instead of confusing them later.
var ReservedRendezvousEnvNames = []string{"MASTER_ADDR", "MASTER_PORT", "WORLD_SIZE", "NNODES", "NODE_RANK"}

func (r *RunRole) validateTemplate() error {
	spec := &r.Template.Spec
	if len(spec.Containers) == 0 {
		return fmt.Errorf("spec.roles[0].template must define at least one container")
	}
	for i := range spec.Containers {
		for _, e := range spec.Containers[i].Env {
			for _, reserved := range ReservedRendezvousEnvNames {
				if e.Name == reserved {
					return fmt.Errorf("spec.roles[0].template: container %q sets env %q, which jobtree owns for distributed-training rendezvous (R9 9A-2) — remove it", spec.Containers[i].Name, e.Name)
				}
			}
		}
	}
	target := r.GPUTargetContainerIndex()
	if target < 0 || spec.Containers[target].Image == "" {
		return fmt.Errorf("spec.roles[0].template: the GPU-target container (named %q, else the first) must set a non-empty image", GPUTargetContainerName)
	}
	if spec.NodeName != "" {
		return fmt.Errorf("spec.roles[0].template.spec.nodeName is owned by jobtree and must not be set")
	}
	if spec.SchedulerName != "" {
		return fmt.Errorf("spec.roles[0].template.spec.schedulerName is owned by jobtree and must not be set")
	}
	if spec.RestartPolicy != "" {
		return fmt.Errorf("spec.roles[0].template.spec.restartPolicy is owned by jobtree (forced to Never) and must not be set")
	}
	return nil
}

// Validate checks the follow section's field-level rules. Existence of the
// referenced runs and cycle detection are cross-object judgments enforced at
// admission by the controller (the webhook has no cluster view).
func (f *RunFollow) Validate(selfName string) error {
	if len(f.After) == 0 {
		return fmt.Errorf("follow.after must list at least one run when follow is set")
	}
	seen := make(map[string]struct{}, len(f.After))
	for _, name := range f.After {
		if name == "" {
			return fmt.Errorf("follow.after entries must be non-empty")
		}
		if selfName != "" && name == selfName {
			return fmt.Errorf("a run cannot follow itself (%q)", name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("follow.after lists %q more than once", name)
		}
		seen[name] = struct{}{}
	}
	switch f.OnUpstreamFailure {
	case "", OnUpstreamFailureWait, OnUpstreamFailureFail:
	default:
		return fmt.Errorf("follow.onUpstreamFailure must be %q or %q when set", OnUpstreamFailureWait, OnUpstreamFailureFail)
	}
	if f.UpstreamFailureGrace != nil && f.UpstreamFailureGrace.Duration < 0 {
		return fmt.Errorf("follow.upstreamFailureGrace must be non-negative")
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

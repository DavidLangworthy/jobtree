// Package lowering maps a jobtree Run onto the workload primitive it borrows
// from Kubernetes SIG Batch: a JobSet. This is the "body" seam of the Option C
// architecture (docs/project/borrow-vs-build.md §5): the Run stays jobtree's
// researcher API and carries the brain (owner, budget, funding, follow, roles),
// while a JobSet supplies the real pods, roles, headless-service DNS, and
// success/failure/restart semantics.
//
// This file is a SKELETON. It establishes the package, the function signature,
// and the documented mapping contract so downstream tasks (JOBSET-2..JOBSET-9)
// have a stable seam to fill in — it is deliberately NOT wired into the Bridge
// yet, because materialization/placement is Track A (PLUGIN) work. The body of
// LowerToJobSet is a TODO guarded by ErrNotImplemented so callers and tests can
// compile against the seam today.
package lowering

import (
	"errors"

	"k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

var (
	// ErrNotImplemented is returned by LowerToJobSet until JOBSET-5 fills in the
	// real Run→JobSet mapping. It is blocked on JOBSET-3 (vendor
	// sigs.k8s.io/jobset so the typed JobSet API is importable) and JOBSET-4
	// (the go/no-go spike on suspend/elastic-parallelism/successPolicy). Keeping
	// the seam behind an explicit sentinel means no caller silently ships a
	// half-lowered workload.
	ErrNotImplemented = errors.New("lowering: LowerToJobSet not yet implemented (blocked on JOBSET-3/JOBSET-4)")

	// ErrNoRoles is returned when a Run has no role to lower. A Run with zero
	// roles still uses the legacy pause-pod path during the JOBSET transition;
	// it cannot be lowered to a JobSet.
	ErrNoRoles = errors.New("lowering: run has no roles to lower")
)

// LowerToJobSet lowers a Run into the JobSet that materializes its workload.
//
// The return type is runtime.Object because the concrete type — a
// *jobsetv1alpha2.JobSet from sigs.k8s.io/jobset — is not importable until
// JOBSET-3 vendors the module. JOBSET-5 tightens the signature to
// (*jobsetv1alpha2.JobSet, error) and fills the body.
//
// The mapping this function will implement (roles → replicatedJobs):
//
//   - One JobSet per Run, namespace/name derived from the Run.
//   - Each RunRole becomes exactly one JobSet ReplicatedJob:
//   - replicas             = 1
//   - parallelism/completions = role.Width   (the gang; JobSet KEP-463
//     later resizes parallelism live for elastic width)
//   - the ReplicatedJob's pod template is a deep copy of role.Template with
//     the jobtree-owned fields overlaid:
//   - spec.schedulerName = "jobtree"   (Track A's plugin places it)
//   - spec.nodeName      left UNSET    (never pin; the plugin binds)
//   - spec.restartPolicy = Never       (a Succeeded pod is the gang signal)
//   - metadata.namespace/name forced by JobSet's own indexing
//   - labels: LabelRunName / LabelGroupIndex / LabelRunRole merged in
//     (see pkg/binder), researcher labels preserved
//   - resources.limits["nvidia.com/gpu"] == requests == role.GPUsPerPod on
//     the GPU-target container (RunRole.GPUTargetContainerIndex)
//   - rendezvous env (MASTER_ADDR via headless DNS, WORLD_SIZE, NNODES,
//     NODE_RANK, ...) appended only when role.Width > 1
//   - JobSet successPolicy{All} + a real failurePolicy so a Failed pod is
//     terminal rather than hanging the Run forever.
//
// v1 admits exactly one role (validated in the webhook), so the current shape
// lowers a single-role Run; the loop over roles is written to extend to N roles
// additively when multi-role RL lands.
func LowerToJobSet(run *v1.Run) (runtime.Object, error) {
	if run == nil || len(run.Spec.Roles) == 0 {
		return nil, ErrNoRoles
	}
	// TODO(JOBSET-5): build and return the *jobsetv1alpha2.JobSet described
	// above, one ReplicatedJob per role. Blocked on JOBSET-3 (vendor
	// sigs.k8s.io/jobset) and JOBSET-4 (spike). Until then the seam exists but
	// produces nothing, so no caller ships a partially-lowered workload.
	return nil, ErrNotImplemented
}

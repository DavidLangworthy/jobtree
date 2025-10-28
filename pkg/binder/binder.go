package binder

import (
	"fmt"
	"strings"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/pack"
)

const (
	// LabelRunName marks pods and leases tied to a Run.
	LabelRunName = "rq.davidlangworthy.io/run"
	// LabelGroupIndex marks the logical group index.
	LabelGroupIndex = "rq.davidlangworthy.io/group-index"
	// LabelRunRole marks whether a pod is active, borrowed, or spare.
	LabelRunRole = "rq.davidlangworthy.io/role"
)

const (
	RoleActive   = "Active"
	RoleBorrowed = "Borrowed"
	RoleSpare    = "Spare"
)

// Request gathers the context required to materialize pods and leases for a Run.
type Request struct {
	Run       *v1.Run
	CoverPlan cover.Plan
	PackPlan  pack.Plan
	Now       time.Time
}

// Result contains the Kubernetes objects that should be created.
type Result struct {
	Pods   []PodManifest
	Leases []v1.Lease
}

// PodManifest captures the minimal data needed to create a pod-like workload.
type PodManifest struct {
	Namespace string
	Name      string
	NodeName  string
	GPUs      int
	Labels    map[string]string
}

// Materialize constructs pods and leases for the provided request.
func Materialize(req Request) (Result, error) {
	if req.Run == nil {
		return Result{}, fmt.Errorf("run must be provided")
	}
	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	if len(req.PackPlan.Groups) == 0 {
		return Result{}, fmt.Errorf("pack plan has no groups")
	}
	if len(req.CoverPlan.Segments) == 0 {
		return Result{}, fmt.Errorf("cover plan has no segments")
	}

	segments := expandSegments(req.CoverPlan)
	if len(segments) == 0 {
		return Result{}, fmt.Errorf("cover plan resolved to zero quantity")
	}

	var pods []PodManifest
	var leases []v1.Lease
	run := req.Run

	for _, group := range req.PackPlan.Groups {
		allocationNodes := flattenAllocations(group.NodePlacements)
		offset := 0
		remaining := group.Size
		for remaining > 0 {
			seg := segments[0]
			take := min(seg.remaining, remaining)
			if take == 0 {
				return Result{}, fmt.Errorf("segment with zero remaining while GPUs still unassigned")
			}
			sliceNodes := allocationNodes[offset : offset+take]

			role := RoleActive
			if seg.segment.Borrowed {
				role = RoleBorrowed
			}
			pod := buildPod(run, group, sliceNodes, role)
			pods = append(pods, pod)

			lease := buildLease(run, group, sliceNodes, seg, req.Now, role)
			leases = append(leases, lease)

			remaining -= take
			offset += take
			seg.remaining -= take
			if seg.remaining == 0 {
				segments = segments[1:]
				if len(segments) == 0 && (remaining > 0 || offset < len(allocationNodes)) {
					return Result{}, fmt.Errorf("cover plan exhausted before assignments completed")
				}
			} else {
				segments[0] = seg
			}
		}
		if offset != len(allocationNodes) {
			// Each GPU must be consumed exactly once.
			return Result{}, fmt.Errorf("placement allocation mismatch for group %d", group.GroupIndex)
		}
	}

	for _, group := range req.PackPlan.Groups {
		if group.Spares <= 0 {
			continue
		}
		spareNodes := flattenAllocations(group.SparePlacements)
		if len(spareNodes) == 0 {
			return Result{}, fmt.Errorf("group %d requested spares but no placements provided", group.GroupIndex)
		}
		offset := 0
		remaining := len(spareNodes)
		for remaining > 0 {
			if len(segments) == 0 {
				return Result{}, fmt.Errorf("cover plan exhausted before assigning spares")
			}
			seg := segments[0]
			take := min(seg.remaining, remaining)
			if take == 0 {
				return Result{}, fmt.Errorf("segment with zero remaining while assigning spares")
			}
			sliceNodes := spareNodes[offset : offset+take]

			pod := buildPod(run, group, sliceNodes, RoleSpare)
			pods = append(pods, pod)

			lease := buildLease(run, group, sliceNodes, seg, req.Now, RoleSpare)
			leases = append(leases, lease)

			remaining -= take
			offset += take
			seg.remaining -= take
			if seg.remaining == 0 {
				segments = segments[1:]
				if len(segments) == 0 && (remaining > 0 || offset < len(spareNodes)) {
					return Result{}, fmt.Errorf("cover plan exhausted before spare assignments completed")
				}
			} else {
				segments[0] = seg
			}
		}
		if offset != len(spareNodes) {
			return Result{}, fmt.Errorf("spare allocation mismatch for group %d", group.GroupIndex)
		}
	}

	if len(segments) > 0 && segments[0].remaining > 0 {
		return Result{}, fmt.Errorf("unused cover quantity remains after placement")
	}

	return Result{Pods: pods, Leases: leases}, nil
}

type segmentCursor struct {
	segment   cover.Segment
	remaining int
}

func expandSegments(plan cover.Plan) []segmentCursor {
	cursors := make([]segmentCursor, 0, len(plan.Segments))
	for _, seg := range plan.Segments {
		if seg.Quantity <= 0 {
			continue
		}
		cursors = append(cursors, segmentCursor{segment: seg, remaining: int(seg.Quantity)})
	}
	return cursors
}

func flattenAllocations(allocs []pack.NodeAllocation) []nodeSlot {
	var result []nodeSlot
	for _, alloc := range allocs {
		for i := 0; i < alloc.GPUs; i++ {
			result = append(result, nodeSlot{
				node:    alloc.Node,
				ordinal: i,
			})
		}
	}
	return result
}

type nodeSlot struct {
	node    string
	ordinal int
}

func buildPod(run *v1.Run, group pack.GroupPlacement, slots []nodeSlot, role string) PodManifest {
	// All slots belong to the same node allocation chunk by construction.
	nodeName := slots[0].node
	gpuCount := len(slots)
	labels := map[string]string{
		LabelRunName:    run.Name,
		LabelGroupIndex: fmt.Sprintf("%d", group.GroupIndex),
		LabelRunRole:    role,
	}
	suffix := fmt.Sprintf("%s-%s", strings.ToLower(role), nodeName)
	return PodManifest{
		Namespace: run.Namespace,
		Name:      fmt.Sprintf("%s-g%02d-%s", run.Name, group.GroupIndex, suffix),
		NodeName:  nodeName,
		GPUs:      gpuCount,
		Labels:    labels,
	}
}

func buildLease(run *v1.Run, group pack.GroupPlacement, slots []nodeSlot, seg segmentCursor, now time.Time, role string) v1.Lease {
	nodes := make([]string, len(slots))
	for i, slot := range slots {
		nodes[i] = fmt.Sprintf("%s#%d", slot.node, slot.ordinal)
	}
	if role == RoleActive && seg.segment.Borrowed {
		role = RoleBorrowed
	}
	lease := v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      fmt.Sprintf("%s-g%02d-%s-%d", run.Name, group.GroupIndex, seg.segment.EnvelopeName, now.UnixNano()),
			Labels: map[string]string{
				LabelRunName:    run.Name,
				LabelGroupIndex: fmt.Sprintf("%d", group.GroupIndex),
				LabelRunRole:    role,
			},
		},
		Spec: v1.LeaseSpec{
			Owner: run.Spec.Owner,
			RunRef: v1.RunReference{
				Name:      run.Name,
				Namespace: run.Namespace,
			},
			Slice: v1.LeaseSlice{
				Nodes: nodes,
				Role:  role,
			},
			Interval: v1.LeaseInterval{
				Start: v1.NewTime(now),
			},
			PaidByEnvelope: seg.segment.EnvelopeName,
			Reason:         "Start",
		},
	}
	return lease
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

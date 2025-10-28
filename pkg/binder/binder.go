package binder

import (
	"fmt"
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

			pod := buildPod(run, group, sliceNodes)
			pods = append(pods, pod)

			lease := buildLease(run, group, sliceNodes, seg, req.Now)
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

func buildPod(run *v1.Run, group pack.GroupPlacement, slots []nodeSlot) PodManifest {
	// All slots belong to the same node allocation chunk by construction.
	nodeName := slots[0].node
	gpuCount := len(slots)
	labels := map[string]string{
		LabelRunName:    run.Name,
		LabelGroupIndex: fmt.Sprintf("%d", group.GroupIndex),
	}
	return PodManifest{
		Namespace: run.Namespace,
		Name:      fmt.Sprintf("%s-g%02d-%s", run.Name, group.GroupIndex, nodeName),
		NodeName:  nodeName,
		GPUs:      gpuCount,
		Labels:    labels,
	}
}

func buildLease(run *v1.Run, group pack.GroupPlacement, slots []nodeSlot, seg segmentCursor, now time.Time) v1.Lease {
	nodes := make([]string, len(slots))
	for i, slot := range slots {
		nodes[i] = fmt.Sprintf("%s#%d", slot.node, slot.ordinal)
	}
	role := "Active"
	if seg.segment.Borrowed {
		role = "Borrowed"
	}
	lease := v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      fmt.Sprintf("%s-g%02d-%s-%d", run.Name, group.GroupIndex, seg.segment.EnvelopeName, now.UnixNano()),
			Labels: map[string]string{
				LabelRunName:    run.Name,
				LabelGroupIndex: fmt.Sprintf("%d", group.GroupIndex),
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

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
	// LabelRunRole marks whether a pod is active or a spare.
	LabelRunRole = "rq.davidlangworthy.io/role"
)

// Lease roles are facts about the slice (R15): Active work versus held
// Spares. Funding class (owned/shared/borrowed/unfunded) is never a role —
// it is derived by pkg/funding from the payer recorded on the lease.
const (
	RoleActive = "Active"
	RoleSpare  = "Spare"
)

// Request gathers the context required to materialize pods and leases for a Run.
type Request struct {
	Run              *v1.Run
	CoverPlan        cover.Plan
	PackPlan         pack.Plan
	Now              time.Time
	GroupIndexOffset int
	LeaseReason      string
	// NameSeed starts the per-materialization sequence number. Callers pass
	// the number of leases that already exist for the run, so names from
	// successive materializations cannot collide even at the same clock
	// reading (shrink-then-grow can reuse group indices).
	NameSeed int
}

// Result contains the Kubernetes objects that should be created.
type Result struct {
	Pods   []PodManifest
	Leases []v1.Lease
}

// PodPhaseSucceeded is the workload-pod phase that signals a slice finished.
// It mirrors corev1.PodSucceeded as a plain string so the engine and the
// binder need no Kubernetes API dependency.
const PodPhaseSucceeded = "Succeeded"

// PodManifest captures the minimal data needed to create a pod-like workload.
// Phase is populated only for pods loaded from the cluster (empty for pods the
// binder is about to create); the run controller reads it to detect gang
// completion.
type PodManifest struct {
	Namespace string
	Name      string
	NodeName  string
	GPUs      int
	Labels    map[string]string
	Phase     string
}

// Materialize constructs pods and leases for the provided request. Node
// allocation chunks and cover segments are walked as two cursors, emitting
// one pod/lease pair per (chunk ∩ segment) intersection, so a slice never
// spans two nodes and every slice is funded by exactly one segment.
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

	reason := req.LeaseReason
	if reason == "" {
		reason = "Start"
	}
	m := &materializer{run: req.Run, now: req.Now, reason: reason, seq: req.NameSeed}

	for _, group := range req.PackPlan.Groups {
		allocated, err := sumChunks(group.NodePlacements, group.GroupIndex)
		if err != nil {
			return Result{}, err
		}
		if allocated != group.Size {
			return Result{}, fmt.Errorf("placement allocation mismatch for group %d", group.GroupIndex)
		}
		segments, err = m.assign(group.GroupIndex+req.GroupIndexOffset, group.NodePlacements, segments, "")
		if err != nil {
			return Result{}, err
		}
	}

	for _, group := range req.PackPlan.Groups {
		if group.Spares <= 0 {
			continue
		}
		if len(group.SparePlacements) == 0 {
			return Result{}, fmt.Errorf("group %d requested spares but no placements provided", group.GroupIndex)
		}
		allocated, err := sumChunks(group.SparePlacements, group.GroupIndex)
		if err != nil {
			return Result{}, err
		}
		if allocated != group.Spares {
			return Result{}, fmt.Errorf("spare allocation mismatch for group %d", group.GroupIndex)
		}
		segments, err = m.assign(group.GroupIndex+req.GroupIndexOffset, group.SparePlacements, segments, RoleSpare)
		if err != nil {
			return Result{}, err
		}
	}

	if len(segments) > 0 {
		return Result{}, fmt.Errorf("unused cover quantity remains after placement")
	}

	return Result{Pods: m.pods, Leases: m.leases}, nil
}

// sumChunks totals an allocation list, rejecting non-positive chunks (a
// negative chunk would silently cancel out against the group-size check).
func sumChunks(allocs []pack.NodeAllocation, groupIndex int) (int, error) {
	total := 0
	for _, chunk := range allocs {
		if chunk.GPUs <= 0 {
			return 0, fmt.Errorf("non-positive placement chunk (%d GPUs on %s) for group %d", chunk.GPUs, chunk.Node, groupIndex)
		}
		total += chunk.GPUs
	}
	return total, nil
}

// materializer accumulates pods and leases and hands out the monotonic
// sequence number that keeps names unique within one materialization.
type materializer struct {
	run    *v1.Run
	now    time.Time
	reason string
	seq    int
	pods   []PodManifest
	leases []v1.Lease
}

// assign walks allocation chunks and cover segments as two cursors. Each
// step consumes the smaller of (GPUs left in the current chunk, GPUs left in
// the current segment) and emits one pod/lease pair for that intersection.
// fixedRole overrides the segment-derived role (used for spares). It returns
// the unconsumed segment cursors.
func (m *materializer) assign(groupIndex int, allocs []pack.NodeAllocation, segments []segmentCursor, fixedRole string) ([]segmentCursor, error) {
	for _, chunk := range allocs {
		offset := 0
		for offset < chunk.GPUs {
			if len(segments) == 0 {
				return nil, fmt.Errorf("cover plan exhausted before assignments completed")
			}
			seg := segments[0]
			take := minInt(seg.remaining, chunk.GPUs-offset)
			slots := make([]nodeSlot, take)
			for i := range slots {
				slots[i] = nodeSlot{node: chunk.Node, ordinal: offset + i}
			}

			role := fixedRole
			if role == "" {
				role = RoleActive
			}

			m.pods = append(m.pods, m.buildPod(groupIndex, slots, role))
			m.leases = append(m.leases, m.buildLease(groupIndex, slots, seg.segment, role))
			m.seq++

			offset += take
			seg.remaining -= take
			if seg.remaining == 0 {
				segments = segments[1:]
			} else {
				segments[0] = seg
			}
		}
	}
	return segments, nil
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

type nodeSlot struct {
	node    string
	ordinal int
}

func (m *materializer) buildPod(groupIndex int, slots []nodeSlot, role string) PodManifest {
	// assign only ever emits slices cut from a single chunk, so every slot
	// shares one node.
	nodeName := slots[0].node
	labels := map[string]string{
		LabelRunName:    m.run.Name,
		LabelGroupIndex: fmt.Sprintf("%d", groupIndex),
		LabelRunRole:    role,
	}
	return PodManifest{
		Namespace: m.run.Namespace,
		Name:      fmt.Sprintf("%s-g%02d-%s-%s-%d", m.run.Name, groupIndex, strings.ToLower(role), nodeName, m.seq),
		NodeName:  nodeName,
		GPUs:      len(slots),
		Labels:    labels,
	}
}

func (m *materializer) buildLease(groupIndex int, slots []nodeSlot, seg cover.Segment, role string) v1.Lease {
	nodes := make([]string, len(slots))
	for i, slot := range slots {
		nodes[i] = fmt.Sprintf("%s#%d", slot.node, slot.ordinal)
	}
	return v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: m.run.Namespace,
			// The budget name qualifies envelope names (which repeat across
			// budgets); the seeded sequence number makes names unique within
			// and across materializations, and the nanosecond timestamp is a
			// second line of defense should a caller reuse a seed.
			Name: fmt.Sprintf("%s-g%02d-%s-%s-%d-%d", m.run.Name, groupIndex, seg.BudgetName, seg.EnvelopeName, m.now.UnixNano(), m.seq),
			Labels: map[string]string{
				LabelRunName:    m.run.Name,
				LabelGroupIndex: fmt.Sprintf("%d", groupIndex),
				LabelRunRole:    role,
			},
		},
		Spec: v1.LeaseSpec{
			Owner: seg.Owner,
			RunRef: v1.RunReference{
				Name:      m.run.Name,
				Namespace: m.run.Namespace,
			},
			Slice: v1.LeaseSlice{
				Nodes: nodes,
				Role:  role,
			},
			Interval: v1.LeaseInterval{
				Start: v1.NewTime(m.now),
			},
			PaidByBudget:   seg.BudgetName,
			PaidByEnvelope: seg.EnvelopeName,
			Reason:         m.reason,
		},
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package pack

import (
	"fmt"
	"sort"

	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// Request captures the inputs needed to compute a placement plan.
type Request struct {
	Flavor                string
	TotalGPUs             int
	GroupGPUs             *int
	AllowCrossGroupSpread bool
}

// FailureReason explains why planning failed.
type FailureReason string

const (
	FailureReasonInvalidRequest       FailureReason = "InvalidRequest"
	FailureReasonInsufficientTopology FailureReason = "InsufficientTopology"
	FailureReasonInsufficientCapacity FailureReason = "InsufficientCapacity"
)

// PlanError is returned when planning fails.
type PlanError struct {
	Reason FailureReason
	Msg    string
}

func (e *PlanError) Error() string { return e.Msg }

// NodeAllocation is an assignment of GPUs on a specific node.
type NodeAllocation struct {
	Node string
	GPUs int
}

// GroupPlacement captures where a logical group of GPUs will run.
type GroupPlacement struct {
	GroupIndex     int
	Size           int
	Domain         topology.DomainKey
	NodePlacements []NodeAllocation
}

// Plan is the outcome of a packing request.
type Plan struct {
	Flavor    string
	TotalGPUs int
	Groups    []GroupPlacement
	Residual  map[topology.DomainKey]int
}

// Planner chooses placements for run groups on available topology.
func Planner(snapshot *topology.Snapshot, req Request) (Plan, error) {
	if snapshot == nil {
		return Plan{}, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "snapshot is nil"}
	}
	if req.TotalGPUs <= 0 {
		return Plan{}, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "totalGPUs must be positive"}
	}
	if req.Flavor == "" {
		return Plan{}, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "flavor must be set"}
	}
	if req.Flavor != snapshot.Flavor {
		return Plan{}, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "snapshot flavor mismatch"}
	}

	work := snapshot.Clone()
	if !req.AllowCrossGroupSpread {
		return planSingleDomain(work, req)
	}
	if req.GroupGPUs != nil {
		return planWithGroups(work, req)
	}
	return planFillDomains(work, req)
}

func planSingleDomain(snapshot *topology.Snapshot, req Request) (Plan, error) {
	var candidate *topology.Domain
	sorted := snapshot.SortedDomains()
	for _, dom := range sorted {
		if dom.FreeGPUs() >= req.TotalGPUs {
			candidate = dom
			break
		}
	}
	if candidate == nil {
		return Plan{}, &PlanError{Reason: FailureReasonInsufficientTopology, Msg: "no single domain can satisfy request"}
	}
	groups := deriveGroups(req.TotalGPUs, req.GroupGPUs)
	var placements []GroupPlacement
	for idx, size := range groups {
		allocs, err := allocateInDomain(candidate, size)
		if err != nil {
			return Plan{}, err
		}
		placements = append(placements, GroupPlacement{
			GroupIndex:     idx,
			Size:           size,
			Domain:         candidate.Key,
			NodePlacements: allocs,
		})
	}
	residual := map[topology.DomainKey]int{}
	for _, dom := range snapshot.Domains {
		residual[dom.Key] = dom.FreeGPUs()
	}
	return Plan{Flavor: req.Flavor, TotalGPUs: req.TotalGPUs, Groups: placements, Residual: residual}, nil
}

func planWithGroups(snapshot *topology.Snapshot, req Request) (Plan, error) {
	groups := deriveGroups(req.TotalGPUs, req.GroupGPUs)
	if len(groups) == 0 {
		return Plan{}, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "no groups derived"}
	}
	var placements []GroupPlacement
	domainUsage := make(map[*topology.Domain]int)
	for idx, size := range groups {
		sorted := snapshot.SortedDomains()
		dom := chooseDomainForGroup(sorted, domainUsage, size)
		if dom == nil {
			return Plan{}, &PlanError{Reason: FailureReasonInsufficientCapacity, Msg: fmt.Sprintf("insufficient capacity for group %d", idx)}
		}
		allocs, err := allocateInDomain(dom, size)
		if err != nil {
			return Plan{}, err
		}
		domainUsage[dom] += size
		placements = append(placements, GroupPlacement{
			GroupIndex:     idx,
			Size:           size,
			Domain:         dom.Key,
			NodePlacements: allocs,
		})
	}
	residual := make(map[topology.DomainKey]int)
	for _, dom := range snapshot.Domains {
		residual[dom.Key] = dom.FreeGPUs()
	}
	return Plan{Flavor: req.Flavor, TotalGPUs: req.TotalGPUs, Groups: placements, Residual: residual}, nil
}

func planFillDomains(snapshot *topology.Snapshot, req Request) (Plan, error) {
	remaining := req.TotalGPUs
	var placements []GroupPlacement
	groupIndex := 0
	for remaining > 0 {
		sorted := snapshot.SortedDomains()
		var dom *topology.Domain
		for _, candidate := range sorted {
			if candidate.FreeGPUs() > 0 {
				dom = candidate
				break
			}
		}
		if dom == nil {
			return Plan{}, &PlanError{Reason: FailureReasonInsufficientCapacity, Msg: "insufficient capacity"}
		}
		assign := dom.FreeGPUs()
		if assign > remaining {
			assign = remaining
		}
		allocs, err := allocateInDomain(dom, assign)
		if err != nil {
			return Plan{}, err
		}
		placements = append(placements, GroupPlacement{
			GroupIndex:     groupIndex,
			Size:           assign,
			Domain:         dom.Key,
			NodePlacements: allocs,
		})
		remaining -= assign
		groupIndex++
	}
	residual := make(map[topology.DomainKey]int)
	for _, dom := range snapshot.Domains {
		residual[dom.Key] = dom.FreeGPUs()
	}
	return Plan{Flavor: req.Flavor, TotalGPUs: req.TotalGPUs, Groups: placements, Residual: residual}, nil
}

func chooseDomainForGroup(domains []*topology.Domain, usage map[*topology.Domain]int, size int) *topology.Domain {
	var candidate *topology.Domain
	for _, dom := range domains {
		if dom.FreeGPUs() < size {
			continue
		}
		if usage[dom] == 0 {
			continue
		}
		if candidate == nil || dom.FreeGPUs() > candidate.FreeGPUs() || (dom.FreeGPUs() == candidate.FreeGPUs() && dom.Key.String() < candidate.Key.String()) {
			candidate = dom
		}
	}
	if candidate != nil {
		return candidate
	}
	for _, dom := range domains {
		if dom.FreeGPUs() < size {
			continue
		}
		if candidate == nil || dom.FreeGPUs() > candidate.FreeGPUs() || (dom.FreeGPUs() == candidate.FreeGPUs() && dom.Key.String() < candidate.Key.String()) {
			candidate = dom
		}
	}
	return candidate
}

func allocateInDomain(domain *topology.Domain, amount int) ([]NodeAllocation, error) {
	if amount <= 0 {
		return nil, &PlanError{Reason: FailureReasonInvalidRequest, Msg: "group size must be positive"}
	}
	if domain.FreeGPUs() < amount {
		return nil, &PlanError{Reason: FailureReasonInsufficientCapacity, Msg: "domain does not have enough capacity"}
	}
	nodes := make([]*topology.Node, len(domain.Nodes))
	copy(nodes, domain.Nodes)
	topology.SortNodesByFree(nodes)
	remaining := amount
	var allocs []NodeAllocation
	for _, node := range nodes {
		if remaining == 0 {
			break
		}
		free := node.FreeGPUs()
		if free == 0 {
			continue
		}
		take := free
		if take > remaining {
			take = remaining
		}
		node.Used += take
		allocs = append(allocs, NodeAllocation{Node: node.Name, GPUs: take})
		remaining -= take
	}
	if remaining > 0 {
		return nil, &PlanError{Reason: FailureReasonInsufficientCapacity, Msg: "insufficient node capacity"}
	}
	sort.Slice(allocs, func(i, j int) bool {
		if allocs[i].GPUs == allocs[j].GPUs {
			return allocs[i].Node < allocs[j].Node
		}
		return allocs[i].GPUs > allocs[j].GPUs
	})
	return allocs, nil
}

func deriveGroups(total int, groupSizePtr *int) []int {
	if groupSizePtr == nil {
		return []int{total}
	}
	groupSize := *groupSizePtr
	if groupSize <= 0 {
		return nil
	}
	var groups []int
	remaining := total
	for remaining > 0 {
		size := groupSize
		if size > remaining {
			size = remaining
		}
		groups = append(groups, size)
		remaining -= size
	}
	return groups
}

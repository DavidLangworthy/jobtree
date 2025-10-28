package topology

import (
	"fmt"
	"sort"
)

// DomainKey identifies a fast-fabric domain scoped by region and cluster.
type DomainKey struct {
	Region  string
	Cluster string
	Fabric  string
}

func (k DomainKey) String() string {
	return fmt.Sprintf("%s/%s/%s", k.Region, k.Cluster, k.Fabric)
}

// Node represents a schedulable GPU provider.
type Node struct {
	Name     string
	Labels   map[string]string
	Capacity int
	Used     int
}

// FreeGPUs returns remaining capacity on the node.
func (n *Node) FreeGPUs() int {
	if n.Capacity <= n.Used {
		return 0
	}
	return n.Capacity - n.Used
}

// Domain aggregates nodes that share a fast-fabric interconnect.
type Domain struct {
	Key    DomainKey
	Flavor string
	Nodes  []*Node
}

// TotalGPUs returns the total GPUs (used + free) in the domain.
func (d *Domain) TotalGPUs() int {
	total := 0
	for _, node := range d.Nodes {
		total += node.Capacity
	}
	return total
}

// FreeGPUs returns the remaining capacity across the domain.
func (d *Domain) FreeGPUs() int {
	free := 0
	for _, node := range d.Nodes {
		free += node.FreeGPUs()
	}
	return free
}

// Snapshot captures the available domains for a particular GPU flavor.
type Snapshot struct {
	Flavor  string
	Domains []*Domain
	// index for quick lookups by domain key.
	byKey map[DomainKey]*Domain
}

// TotalFreeGPUs returns the aggregate free capacity across all domains.
func (s *Snapshot) TotalFreeGPUs() int {
	if s == nil {
		return 0
	}
	total := 0
	for _, dom := range s.Domains {
		total += dom.FreeGPUs()
	}
	return total
}

// LargestDomain returns the domain with the highest free GPU count.
func (s *Snapshot) LargestDomain() (*Domain, bool) {
	if s == nil || len(s.Domains) == 0 {
		return nil, false
	}
	sorted := s.SortedDomains()
	if len(sorted) == 0 {
		return nil, false
	}
	return sorted[0], true
}

// DomainByKey retrieves a domain from the snapshot.
func (s *Snapshot) DomainByKey(key DomainKey) (*Domain, bool) {
	dom, ok := s.byKey[key]
	return dom, ok
}

// SourceNode represents the minimal information needed from a Kubernetes node.
type SourceNode struct {
	Name   string
	Labels map[string]string
	GPUs   int
}

// BuildSnapshotForFlavor constructs a topology snapshot filtering nodes by GPU flavor.
// usage maps node name to GPUs already consumed on that node.
func BuildSnapshotForFlavor(nodes []SourceNode, usage map[string]int, flavor string) (*Snapshot, error) {
	domains := map[DomainKey]*Domain{}
	for _, node := range nodes {
		labels := node.Labels
		if labels[LabelGPUFlavor] != flavor {
			continue
		}
		region, cluster, fabric := labels[LabelRegion], labels[LabelCluster], labels[LabelFabricDomain]
		if region == "" || cluster == "" || fabric == "" {
			return nil, fmt.Errorf("node %q missing topology labels", node.Name)
		}
		if node.GPUs <= 0 {
			continue
		}
		used := 0
		if usage != nil {
			used = usage[node.Name]
		}
		if used < 0 || used > node.GPUs {
			return nil, fmt.Errorf("node %q usage %d exceeds capacity %d", node.Name, used, node.GPUs)
		}
		key := DomainKey{Region: region, Cluster: cluster, Fabric: fabric}
		dom, ok := domains[key]
		if !ok {
			dom = &Domain{Key: key, Flavor: flavor}
			domains[key] = dom
		}
		nodeCopy := &Node{
			Name:     node.Name,
			Labels:   map[string]string{LabelRack: labels[LabelRack]},
			Capacity: node.GPUs,
			Used:     used,
		}
		dom.Nodes = append(dom.Nodes, nodeCopy)
	}

	if len(domains) == 0 {
		return &Snapshot{Flavor: flavor, Domains: nil, byKey: map[DomainKey]*Domain{}}, nil
	}

	doms := make([]*Domain, 0, len(domains))
	for _, dom := range domains {
		sort.Slice(dom.Nodes, func(i, j int) bool {
			if dom.Nodes[i].Name == dom.Nodes[j].Name {
				return false
			}
			return dom.Nodes[i].Name < dom.Nodes[j].Name
		})
		doms = append(doms, dom)
	}
	sort.Slice(doms, func(i, j int) bool {
		if doms[i].FreeGPUs() == doms[j].FreeGPUs() {
			return doms[i].Key.String() < doms[j].Key.String()
		}
		return doms[i].FreeGPUs() > doms[j].FreeGPUs()
	})
	byKey := make(map[DomainKey]*Domain, len(doms))
	for _, dom := range doms {
		byKey[dom.Key] = dom
	}
	return &Snapshot{Flavor: flavor, Domains: doms, byKey: byKey}, nil
}

// Clone returns a deep copy of the snapshot. Useful for mutation in packers.
func (s *Snapshot) Clone() *Snapshot {
	if s == nil {
		return nil
	}
	doms := make([]*Domain, len(s.Domains))
	byKey := make(map[DomainKey]*Domain, len(s.Domains))
	for i, dom := range s.Domains {
		domCopy := &Domain{Key: dom.Key, Flavor: dom.Flavor}
		domCopy.Nodes = make([]*Node, len(dom.Nodes))
		for j, node := range dom.Nodes {
			nodeCopy := *node
			nodeCopy.Labels = map[string]string{}
			for k, v := range node.Labels {
				nodeCopy.Labels[k] = v
			}
			domCopy.Nodes[j] = &nodeCopy
		}
		doms[i] = domCopy
		byKey[domCopy.Key] = domCopy
	}
	return &Snapshot{Flavor: s.Flavor, Domains: doms, byKey: byKey}
}

// SortedDomains returns domains ordered by descending free GPUs, breaking ties deterministically.
func (s *Snapshot) SortedDomains() []*Domain {
	doms := make([]*Domain, len(s.Domains))
	copy(doms, s.Domains)
	sort.Slice(doms, func(i, j int) bool {
		if doms[i].FreeGPUs() == doms[j].FreeGPUs() {
			return doms[i].Key.String() < doms[j].Key.String()
		}
		return doms[i].FreeGPUs() > doms[j].FreeGPUs()
	})
	return doms
}

// SortNodesByFree sorts nodes inside a domain by free GPUs (desc) then by name.
func SortNodesByFree(nodes []*Node) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].FreeGPUs() == nodes[j].FreeGPUs() {
			return nodes[i].Name < nodes[j].Name
		}
		return nodes[i].FreeGPUs() > nodes[j].FreeGPUs()
	})
}

// WithUsage returns a new snapshot with updated usage map applied.
func (s *Snapshot) WithUsage(usage map[string]int) *Snapshot {
	clone := s.Clone()
	if usage == nil {
		return clone
	}
	for _, dom := range clone.Domains {
		for _, node := range dom.Nodes {
			if used, ok := usage[node.Name]; ok {
				node.Used = used
				if node.Used > node.Capacity {
					node.Used = node.Capacity
				}
			}
		}
	}
	return clone
}

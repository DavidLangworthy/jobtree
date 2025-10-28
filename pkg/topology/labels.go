package topology

const (
	// LabelRegion identifies the geographical region for a node.
	LabelRegion = "region"
	// LabelCluster identifies the cluster within a region.
	LabelCluster = "cluster"
	// LabelFabricDomain marks the fast-fabric domain / island for the node.
	LabelFabricDomain = "fabric.domain"
	// LabelRack is an optional rack identifier used as a tie breaker.
	LabelRack = "rack"
	// LabelGPUFlavor declares the GPU flavor that a node provides.
	LabelGPUFlavor = "gpu.flavor"
)

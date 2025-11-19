package v1

// SchemeGroupVersion identifies the API group and version for these types.
var SchemeGroupVersion = struct {
	Group   string
	Version string
}{
	Group:   "rq.davidlangworthy.io",
	Version: "v1",
}

// AddToScheme is a no-op placeholder mirroring controller-runtime scaffolding.
func AddToScheme(interface{}) error { return nil }

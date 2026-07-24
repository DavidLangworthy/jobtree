// Package manifestcorpus is the accept/reject manifest corpus shared by the
// api/v1 unit tests and the envtest webhook suite. Keeping one copy means the
// in-process validation tests and the real-API-server admission tests (R18)
// can never drift apart.
package manifestcorpus

// Case is one manifest plus the expected validation outcome.
type Case struct {
	Name     string
	Manifest string // JSON object without apiVersion/kind
	WantErr  string // empty means accept; otherwise a substring of the rejection
}

// Runs exercises every Run validation rule from raw JSON manifests.
var Runs = []Case{
	{
		Name: "minimal valid run",
		Manifest: `{
			"metadata": {"name": "train", "namespace": "default"},
			"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8}}
		}`,
	},
	{
		Name: "valid malleable run with funding",
		Manifest: `{
			"metadata": {"name": "train", "namespace": "default"},
			"spec": {
				"owner": "org:ai",
				"resources": {"gpuType": "H100-80GB", "totalGPUs": 16},
				"locality": {"groupGPUs": 8},
				"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8},
				"funding": {"allowBorrow": true, "maxBorrowGPUs": 8, "sponsors": ["org:mm"]},
				"sparesPerGroup": 1
			}
		}`,
	},
	{
		Name:     "missing gpuType",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"totalGPUs": 8}}}`,
		WantErr:  "spec.resources.gpuType is required",
	},
	{
		Name:     "zero GPUs",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 0}}}`,
		WantErr:  "totalGPUs must be positive",
	},
	{
		Name: "non-positive groupGPUs",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8},
			"locality": {"groupGPUs": 0}}}`,
		WantErr: "groupGPUs must be positive",
	},
	{
		Name: "malleable min above max",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 16},
			"malleable": {"minTotalGPUs": 32, "maxTotalGPUs": 16, "stepGPUs": 8}}}`,
		WantErr: "minTotalGPUs must be <= maxTotalGPUs",
	},
	{
		Name: "malleable non-positive step",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 16},
			"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 0}}}`,
		WantErr: "stepGPUs must be positive",
	},
	{
		Name: "total above malleable max",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 64},
			"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8}}}`,
		WantErr: "must fall within malleable min/max",
	},
	{
		Name: "total below malleable min",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 4},
			"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8}}}`,
		WantErr: "must fall within malleable min/max",
	},
	{
		Name: "total misaligned with step",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 12},
			"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8}}}`,
		WantErr: "must align with malleable.stepGPUs",
	},
	{
		Name: "desired outside malleable bounds",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 16},
			"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8, "desiredTotalGPUs": 64}}}`,
		WantErr: "desiredTotalGPUs must fall within min/max",
	},
	{
		Name: "non-positive maxBorrowGPUs",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8},
			"funding": {"allowBorrow": true, "maxBorrowGPUs": 0}}}`,
		WantErr: "maxBorrowGPUs must be positive",
	},
	{
		Name: "negative sparesPerGroup",
		Manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8},
			"sparesPerGroup": -1}}`,
		WantErr: "sparesPerGroup must be >= 0",
	},
}

// Budgets exercises every Budget validation rule from raw JSON manifests.
var Budgets = []Case{
	{
		Name: "minimal valid budget",
		Manifest: `{
			"metadata": {"name": "rai"},
			"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}
			]}
		}`,
	},
	{
		Name: "valid budget with window, lending, and aggregate cap",
		Manifest: `{
			"metadata": {"name": "rai"},
			"spec": {"owner": "org:ai:rai",
				"envelopes": [
					{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16,
					 "start": "2026-01-01T00:00:00Z", "end": "2026-02-01T00:00:00Z", "maxGPUHours": 1000,
					 "lending": {"allow": true, "to": ["org:ai:mm"], "maxConcurrency": 4}},
					{"name": "east", "flavor": "H100-80GB", "selector": {"region": "us-east"}, "concurrency": 8}
				],
				"aggregateCaps": [
					{"name": "all", "flavor": "H100-80GB", "envelopes": ["west", "east"], "maxConcurrency": 20}
				]}
		}`,
	},
	{
		Name: "valid budget with sharing family and none (R15)",
		Manifest: `{
			"metadata": {"name": "rai"},
			"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "shared", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16, "sharing": "family"},
				{"name": "sealed", "flavor": "H100-80GB", "selector": {"region": "us-east"}, "concurrency": 8, "sharing": "none"}
			]}
		}`,
	},
	{
		Name: "envelope invalid sharing mode",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16, "sharing": "everyone"}
		]}}`,
		WantErr: `sharing must be "family" or "none" when set`,
	},
	{
		Name:     "missing owner",
		Manifest: `{"spec": {"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}]}}`,
		WantErr:  "spec.owner is required",
	},
	{
		Name:     "no envelopes",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": []}}`,
		WantErr:  "spec.envelopes must not be empty",
	},
	{
		Name: "duplicate envelope names",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16},
			{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west-2"}, "concurrency": 8}
		]}}`,
		WantErr: `duplicate envelope name "west"`,
	},
	{
		Name: "envelope missing name",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}
		]}}`,
		WantErr: "name is required",
	},
	{
		Name: "envelope missing selector",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"name": "west", "flavor": "H100-80GB", "concurrency": 16}
		]}}`,
		WantErr: "selector must contain at least one label",
	},
	{
		Name: "envelope non-positive concurrency",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 0}
		]}}`,
		WantErr: "concurrency must be positive",
	},
	{
		Name: "envelope window inverted",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16,
			 "start": "2026-02-01T00:00:00Z", "end": "2026-01-01T00:00:00Z"}
		]}}`,
		WantErr: "end must be after start",
	},
	{
		Name: "maxGPUHours exceeds window integral",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 1,
			 "start": "2026-01-01T00:00:00Z", "end": "2026-01-01T10:00:00Z", "maxGPUHours": 100}
		]}}`,
		WantErr: "maxGPUHours exceeds concurrency×window",
	},
	{
		Name: "lending non-positive maxConcurrency",
		Manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
			{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16,
			 "lending": {"allow": true, "maxConcurrency": 0}}
		]}}`,
		WantErr: "lending.maxConcurrency must be positive",
	},
	{
		Name: "aggregate cap references unknown envelope",
		Manifest: `{"spec": {"owner": "org:ai:rai",
			"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
			"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": ["west", "east"]}]}}`,
		WantErr: `references unknown envelope "east"`,
	},
	{
		Name: "aggregate cap with no envelope references",
		Manifest: `{"spec": {"owner": "org:ai:rai",
			"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
			"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": []}]}}`,
		WantErr: "envelopes must reference at least one envelope",
	},
	{
		Name: "aggregate cap missing flavor",
		Manifest: `{"spec": {"owner": "org:ai:rai",
			"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
			"aggregateCaps": [{"name": "all", "envelopes": ["west"]}]}}`,
		WantErr: "flavor is required",
	},
	{
		Name: "aggregate cap missing name",
		Manifest: `{"spec": {"owner": "org:ai:rai",
			"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
			"aggregateCaps": [{"flavor": "H100-80GB", "envelopes": ["west"]}]}}`,
		WantErr: "name is required",
	},
	{
		Name: "aggregate cap duplicate reference",
		Manifest: `{"spec": {"owner": "org:ai:rai",
			"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
			"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": ["west", "west"]}]}}`,
		WantErr: `references envelope "west" more than once`,
	},
	{
		Name: "duplicate aggregate cap names",
		Manifest: `{"spec": {"owner": "org:ai:rai",
			"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
			"aggregateCaps": [
				{"name": "all", "flavor": "H100-80GB", "envelopes": ["west"]},
				{"name": "all", "flavor": "H100-80GB", "envelopes": ["west"]}
			]}}`,
		WantErr: `duplicate aggregate cap name "all"`,
	},
	{
		Name: "aggregate cap non-positive maxConcurrency",
		Manifest: `{"spec": {"owner": "org:ai:rai",
			"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
			"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": ["west"], "maxConcurrency": 0}]}}`,
		WantErr: "maxConcurrency must be positive",
	},
}

package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

// The accept/reject corpus below exercises every validation rule from raw
// JSON manifests, so it can double as the CRD schema conformance suite once
// real CRDs exist (testing plan, Tier 2).

func TestRunManifestValidationCorpus(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		wantErr  string // empty means accept
	}{
		{
			name: "minimal valid run",
			manifest: `{
				"metadata": {"name": "train", "namespace": "default"},
				"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8}}
			}`,
		},
		{
			name: "valid malleable run with funding",
			manifest: `{
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
			name:     "missing owner",
			manifest: `{"spec": {"resources": {"gpuType": "H100-80GB", "totalGPUs": 8}}}`,
			wantErr:  "spec.owner is required",
		},
		{
			name:     "missing gpuType",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"totalGPUs": 8}}}`,
			wantErr:  "spec.resources.gpuType is required",
		},
		{
			name:     "zero GPUs",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 0}}}`,
			wantErr:  "totalGPUs must be positive",
		},
		{
			name: "non-positive groupGPUs",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8},
				"locality": {"groupGPUs": 0}}}`,
			wantErr: "groupGPUs must be positive",
		},
		{
			name: "malleable min above max",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 16},
				"malleable": {"minTotalGPUs": 32, "maxTotalGPUs": 16, "stepGPUs": 8}}}`,
			wantErr: "minTotalGPUs must be <= maxTotalGPUs",
		},
		{
			name: "malleable zero step",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 16},
				"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 0}}}`,
			wantErr: "stepGPUs must be positive",
		},
		{
			name: "total outside malleable range",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 64},
				"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8}}}`,
			wantErr: "must fall within malleable min/max",
		},
		{
			name: "total misaligned with step",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 12},
				"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8}}}`,
			wantErr: "must align with malleable.stepGPUs",
		},
		{
			name: "desired outside malleable range",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 16},
				"malleable": {"minTotalGPUs": 8, "maxTotalGPUs": 32, "stepGPUs": 8, "desiredTotalGPUs": 64}}}`,
			wantErr: "desiredTotalGPUs must fall within min/max",
		},
		{
			name: "non-positive borrow cap",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8},
				"funding": {"allowBorrow": true, "maxBorrowGPUs": 0}}}`,
			wantErr: "maxBorrowGPUs must be positive",
		},
		{
			name: "negative spares",
			manifest: `{"spec": {"owner": "org:ai", "resources": {"gpuType": "H100-80GB", "totalGPUs": 8},
				"sparesPerGroup": -1}}`,
			wantErr: "sparesPerGroup must be >= 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var run Run
			if err := json.Unmarshal([]byte(tc.manifest), &run); err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			run.Default()
			err := run.ValidateCreate()
			checkValidation(t, err, tc.wantErr)
		})
	}
}

func TestBudgetManifestValidationCorpus(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{
			name: "minimal valid budget",
			manifest: `{
				"metadata": {"name": "rai"},
				"spec": {"owner": "org:ai:rai", "envelopes": [
					{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}
				]}
			}`,
		},
		{
			name: "valid budget with window, lending, and aggregate cap",
			manifest: `{
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
			name:     "missing owner",
			manifest: `{"spec": {"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}]}}`,
			wantErr:  "spec.owner is required",
		},
		{
			name:     "no envelopes",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": []}}`,
			wantErr:  "spec.envelopes must not be empty",
		},
		{
			name: "duplicate envelope names",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16},
				{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west-2"}, "concurrency": 8}
			]}}`,
			wantErr: `duplicate envelope name "west"`,
		},
		{
			name: "envelope missing name",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
				{"flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}
			]}}`,
			wantErr: "name is required",
		},
		{
			name: "envelope missing selector",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "west", "flavor": "H100-80GB", "concurrency": 16}
			]}}`,
			wantErr: "selector must contain at least one label",
		},
		{
			name: "envelope non-positive concurrency",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 0}
			]}}`,
			wantErr: "concurrency must be positive",
		},
		{
			name: "envelope window inverted",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16,
				 "start": "2026-02-01T00:00:00Z", "end": "2026-01-01T00:00:00Z"}
			]}}`,
			wantErr: "end must be after start",
		},
		{
			name: "maxGPUHours exceeds window integral",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 1,
				 "start": "2026-01-01T00:00:00Z", "end": "2026-01-01T10:00:00Z", "maxGPUHours": 100}
			]}}`,
			wantErr: "maxGPUHours exceeds concurrency×window",
		},
		{
			name: "lending non-positive maxConcurrency",
			manifest: `{"spec": {"owner": "org:ai:rai", "envelopes": [
				{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16,
				 "lending": {"allow": true, "maxConcurrency": 0}}
			]}}`,
			wantErr: "lending.maxConcurrency must be positive",
		},
		{
			name: "aggregate cap references unknown envelope",
			manifest: `{"spec": {"owner": "org:ai:rai",
				"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
				"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": ["west", "east"]}]}}`,
			wantErr: `references unknown envelope "east"`,
		},
		{
			name: "aggregate cap with no envelope references",
			manifest: `{"spec": {"owner": "org:ai:rai",
				"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
				"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": []}]}}`,
			wantErr: "envelopes must reference at least one envelope",
		},
		{
			name: "aggregate cap missing flavor",
			manifest: `{"spec": {"owner": "org:ai:rai",
				"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
				"aggregateCaps": [{"name": "all", "envelopes": ["west"]}]}}`,
			wantErr: "flavor is required",
		},
		{
			name: "aggregate cap missing name",
			manifest: `{"spec": {"owner": "org:ai:rai",
				"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
				"aggregateCaps": [{"flavor": "H100-80GB", "envelopes": ["west"]}]}}`,
			wantErr: "name is required",
		},
		{
			name: "aggregate cap duplicate reference",
			manifest: `{"spec": {"owner": "org:ai:rai",
				"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
				"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": ["west", "west"]}]}}`,
			wantErr: `references envelope "west" more than once`,
		},
		{
			name: "duplicate aggregate cap names",
			manifest: `{"spec": {"owner": "org:ai:rai",
				"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
				"aggregateCaps": [
					{"name": "all", "flavor": "H100-80GB", "envelopes": ["west"]},
					{"name": "all", "flavor": "H100-80GB", "envelopes": ["west"]}
				]}}`,
			wantErr: `duplicate aggregate cap name "all"`,
		},
		{
			name: "aggregate cap non-positive maxConcurrency",
			manifest: `{"spec": {"owner": "org:ai:rai",
				"envelopes": [{"name": "west", "flavor": "H100-80GB", "selector": {"region": "us-west"}, "concurrency": 16}],
				"aggregateCaps": [{"name": "all", "flavor": "H100-80GB", "envelopes": ["west"], "maxConcurrency": 0}]}}`,
			wantErr: "maxConcurrency must be positive",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var budget Budget
			if err := json.Unmarshal([]byte(tc.manifest), &budget); err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			err := budget.ValidateCreate()
			checkValidation(t, err, tc.wantErr)
		})
	}
}

func checkValidation(t *testing.T, err error, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("expected manifest to be accepted, got: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected rejection containing %q, but manifest was accepted", wantErr)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("expected error containing %q, got: %v", wantErr, err)
	}
}

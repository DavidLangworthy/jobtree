package controllers

// Golden oracle for the pre-plugin ("nodeName binder") commit path.
//
// Each scenario drives the PURE engine (NewRunController + Reconcile, no
// apiserver) through one of the customer-promised behaviors from
// docs/user-guide/researcher-guide.md and freezes the resulting facts —
// run statuses, leases, reservations, and pod manifests — into
// testdata/golden/<name>.json.
//
// The point is the DIFF. Under the corrected funding-commit design
// (docs/project/borrow-vs-build.md §9) the scheduler plugin becomes the sole
// committer: run_controller.Reconcile stops minting leases (the plugin's Bind
// does) and stops pinning nodes (the scheduler places pods). When PLUGIN-2
// re-plumbs that, re-running this test with UPDATE_GOLDEN=1 and reviewing the
// diff is the precise, auditable record of what moved from controller to
// plugin — and anything that changes that SHOULDN'T have (funding class,
// payer attribution, deficit, remedies) shows up as an unintended mismatch.
//
// Regenerate after an intended behavior change:  UPDATE_GOLDEN=1 go test ./controllers/ -run TestGoldenScenarios
//
// The frozen "old" path also lives, runnable end-to-end, at the sibling
// worktree /workspaces/jobtree-legacy (branch legacy/nodename-binder) for
// envtest-level comparison when this pure-engine capture isn't enough.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// goldenSel is the one fabric domain every golden node and envelope lives in.
func goldenSel() map[string]string {
	return map[string]string{
		topology.LabelRegion:       "us-west",
		topology.LabelCluster:      "cluster-a",
		topology.LabelFabricDomain: "island-a",
	}
}

func goldenNode(name string, gpus int) topology.SourceNode {
	labels := goldenSel()
	labels[topology.LabelGPUFlavor] = "H100-80GB"
	return topology.SourceNode{Name: name, Labels: labels, GPUs: gpus}
}

// ---- scenarios (faithful reductions of controllers/run_controller_test.go) ----

// simple-fit: 4-GPU run, one island, binds immediately, no reservation.
func scnSimpleFit() (*ClusterState, time.Time) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "rai"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 8,
			}}},
		}},
		Nodes: []topology.SourceNode{goldenNode("node-a", 4)},
		Runs: map[string]*v1.Run{"default/train-8": {
			ObjectMeta: v1.ObjectMeta{Name: "train-8", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
		}},
	}
	NewRunController(state, runClock{now: now}).Reconcile("default", "train-8")
	return state, now
}

// borrow-sponsor-runs: family out of quota, sponsor lends the shortfall; the
// run goes Running as 96 owned + 32 borrowed (vision is not family, so its
// contribution classes borrowed, not shared).
func scnBorrowSponsorRuns() (*ClusterState, time.Time) {
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	limit := int32(32)
	state := &ClusterState{
		Budgets: []v1.Budget{
			{ObjectMeta: v1.ObjectMeta{Name: "rai"}, Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 96,
			}}}},
			{ObjectMeta: v1.ObjectMeta{Name: "vision"}, Spec: v1.BudgetSpec{Owner: "org:ai:mm:vision", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 64,
				Lending: &v1.LendingPolicy{Allow: true, To: []string{"org:ai:rai", "org:ai:rai:*"}, MaxConcurrency: &limit},
			}}}},
		},
	}
	for i := 0; i < 4; i++ {
		state.Nodes = append(state.Nodes, goldenNode(fmt.Sprintf("node-%d", i), 32))
	}
	maxBorrow, group := int32(64), int32(32)
	state.Runs = map[string]*v1.Run{"default/train-128": {
		ObjectMeta: v1.ObjectMeta{Name: "train-128", Namespace: "default"},
		Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 128},
			Locality: &v1.RunLocality{GroupGPUs: &group},
			Funding:  &v1.RunFunding{AllowBorrow: true, MaxBorrowGPUs: &maxBorrow, Sponsors: []string{"org:ai:mm:vision"}}},
	}}
	NewRunController(state, runClock{now: now}).Reconcile("default", "train-128")
	return state, now
}

// borrow-limited-reservation: the same shape but MaxBorrowGPUs caps below the
// shortfall, so the run cannot fund its full width now and a Reservation is
// written instead of binding.
func scnBorrowLimitedReservation() (*ClusterState, time.Time) {
	now := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{
			{ObjectMeta: v1.ObjectMeta{Name: "rai"}, Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 64,
			}}}},
			{ObjectMeta: v1.ObjectMeta{Name: "vision"}, Spec: v1.BudgetSpec{Owner: "org:ai:mm:vision", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 64,
				Lending: &v1.LendingPolicy{Allow: true, To: []string{"org:ai:rai", "org:ai:rai:*"}},
			}}}},
		},
	}
	for i := 0; i < 4; i++ {
		state.Nodes = append(state.Nodes, goldenNode(fmt.Sprintf("node-b-%d", i), 32))
	}
	maxBorrow, group := int32(8), int32(32)
	state.Runs = map[string]*v1.Run{"default/train-128": {
		ObjectMeta: v1.ObjectMeta{Name: "train-128", Namespace: "default"},
		Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 128},
			Locality: &v1.RunLocality{GroupGPUs: &group},
			Funding:  &v1.RunFunding{AllowBorrow: true, MaxBorrowGPUs: &maxBorrow, Sponsors: []string{"org:ai:mm:vision"}}},
	}}
	NewRunController(state, runClock{now: now}).Reconcile("default", "train-128")
	return state, now
}

// capacity-missing-reservation: budget has headroom but the cluster has too
// few GPUs, so cover+pack cannot satisfy now and a Reservation with a forecast
// (deficit + ETA + remedies) is written.
func scnCapacityMissingReservation() (*ClusterState, time.Time) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{Owner: "org:ai:team", Envelopes: []v1.BudgetEnvelope{{
				Name: "west", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 16,
			}}},
		}},
		Nodes: []topology.SourceNode{goldenNode("node-a", 4)},
		Runs: map[string]*v1.Run{"default/train-8": {
			ObjectMeta: v1.ObjectMeta{Name: "train-8", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8}},
		}},
	}
	NewRunController(state, runClock{now: now}).Reconcile("default", "train-8")
	return state, now
}

// elastic-grow: a malleable run starts at its floor (96) then grows toward
// desired (160) as headroom appears on the next reconcile a minute later.
func scnElasticGrow() (*ClusterState, time.Time) {
	now := time.Date(2024, 2, 2, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 256,
			}}},
		}},
	}
	for i := 0; i < 5; i++ {
		state.Nodes = append(state.Nodes, goldenNode(fmt.Sprintf("node-%d", i), 32))
	}
	desired, group := int32(160), int32(32)
	state.Runs = map[string]*v1.Run{"default/train": {
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 96},
			Locality:  &v1.RunLocality{GroupGPUs: &group},
			Malleable: &v1.RunMalleability{MinTotalGPUs: 96, MaxTotalGPUs: 160, StepGPUs: 32, DesiredTotalGPUs: &desired}},
	}}
	c := NewRunController(state, runClock{now: now})
	c.Reconcile("default", "train")
	c.Clock = runClock{now: now.Add(time.Minute)}
	c.Reconcile("default", "train")
	return state, now
}

// node-failure-swap: a run with spares loses a node; the active ranks swap
// onto the hot standby, reclaiming opportunistic filler squatting there.
func scnNodeFailureSwap() (*ClusterState, time.Time) {
	now := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{Owner: "org:ai:team", Envelopes: []v1.BudgetEnvelope{{
				Name: "west", Flavor: "H100-80GB", Selector: goldenSel(), Concurrency: 8,
			}}},
		}},
		Nodes: []topology.SourceNode{goldenNode("node-a", 4), goldenNode("node-b", 4)},
		Runs: map[string]*v1.Run{"default/run": {
			ObjectMeta: v1.ObjectMeta{Name: "run", Namespace: "default"},
			Spec: v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
				Locality: &v1.RunLocality{GroupGPUs: int32Ptr(4)}, Spares: int32Ptr(2)},
		}},
	}
	c := NewRunController(state, runClock{now: now})
	c.Reconcile("default", "run")

	// Opportunistic filler squatting on the spare's node, reclaimed by the swap.
	var spare *v1.Lease
	for i := range state.Leases {
		if state.Leases[i].Spec.Slice.Role == binder.RoleSpare {
			spare = &state.Leases[i]
			break
		}
	}
	if spare != nil {
		state.Leases = append(state.Leases, v1.Lease{
			ObjectMeta: v1.ObjectMeta{Name: "filler"},
			Spec: v1.LeaseSpec{Owner: "org:ai:other", RunRef: v1.RunReference{Name: "filler", Namespace: "default"},
				Slice:    v1.LeaseSlice{Nodes: append([]string{}, spare.Spec.Slice.Nodes...), Role: binder.RoleActive},
				Interval: v1.LeaseInterval{Start: v1.NewTime(now)}, PaidByEnvelope: "west", Reason: "Start"},
		})
		state.Pods = append(state.Pods, binder.PodManifest{
			Namespace: "default", Name: "filler", NodeName: nodeFromSlot(spare.Spec.Slice.Nodes[0]),
			GPUs:   len(spare.Spec.Slice.Nodes),
			Labels: map[string]string{binder.LabelRunName: "filler", binder.LabelGroupIndex: "0", binder.LabelRunRole: binder.RoleActive},
		})
	}

	failTime := now.Add(5 * time.Minute)
	c.Clock = runClock{now: failTime}
	c.HandleNodeFailure("node-a", failTime)
	return state, now
}

// ---- normalized snapshot ----

type goldenSnapshot struct {
	Scenario     string              `json:"scenario"`
	Runs         []goldenRun         `json:"runs"`
	Leases       []goldenLease       `json:"leases"`
	Reservations []goldenReservation `json:"reservations,omitempty"`
	Pods         []goldenPod         `json:"pods,omitempty"`
}

type goldenRun struct {
	Key                string         `json:"key"`
	Phase              string         `json:"phase"`
	Message            string         `json:"message,omitempty"`
	Width              *goldenWidth   `json:"width,omitempty"`
	Funding            *goldenFunding `json:"funding,omitempty"`
	PendingReservation bool           `json:"pendingReservation,omitempty"`
	HasEarliestStart   bool           `json:"hasEarliestStart,omitempty"`
	HasETA             bool           `json:"hasEta,omitempty"`
}

type goldenWidth struct {
	Min, Max, Desired, Allocated int32
	Pending                      string `json:"pending,omitempty"`
}

// goldenFunding captures the derived funding CLASS (the moat) — counts and
// lenders, not the wall-clock-derived GPU-hour floats.
type goldenFunding struct {
	OwnedGPUs, SharedGPUs, BorrowedGPUs, UnfundedGPUs int32
	Lenders                                           []string `json:"lenders,omitempty"`
}

type goldenLease struct {
	Owner          string   `json:"owner"`
	Run            string   `json:"run"`
	PaidByBudget   string   `json:"paidByBudget,omitempty"`
	PaidByEnvelope string   `json:"paidByEnvelope"`
	CompPath       []string `json:"compPath,omitempty"`
	GPUs           int      `json:"gpus"`
	Role           string   `json:"role"`
	Nodes          []string `json:"nodes"`
	Reason         string   `json:"reason"`
	Closed         bool     `json:"closed,omitempty"`
	ClosureReason  string   `json:"closureReason,omitempty"`
}

type goldenReservation struct {
	Name                  string   `json:"name"`
	Run                   string   `json:"run"`
	State                 string   `json:"state,omitempty"`
	PayingEnvelope        string   `json:"payingEnvelope,omitempty"`
	EarliestStartAfterNow bool     `json:"earliestStartAfterNow"`
	DeficitGPUs           int32    `json:"deficitGpus"`
	Confidence            string   `json:"confidence,omitempty"`
	Remedies              []string `json:"remedies,omitempty"`
}

type goldenPod struct {
	Key      string `json:"key"`
	Run      string `json:"run,omitempty"`
	Role     string `json:"role,omitempty"`
	Group    string `json:"group,omitempty"`
	GPUs     int    `json:"gpus"`
	NodeName string `json:"nodeName"`
}

var tsRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

func snapshot(name string, state *ClusterState, now time.Time) goldenSnapshot {
	snap := goldenSnapshot{Scenario: name}

	for key, run := range state.Runs {
		gr := goldenRun{
			Key:                key,
			Phase:              run.Status.Phase,
			Message:            tsRE.ReplaceAllString(run.Status.Message, "<ts>"),
			PendingReservation: run.Status.PendingReservation != nil,
			HasEarliestStart:   run.Status.EarliestStart != nil,
			HasETA:             run.Status.ETA != nil,
		}
		if w := run.Status.Width; w != nil {
			gr.Width = &goldenWidth{Min: w.Min, Max: w.Max, Desired: w.Desired, Allocated: w.Allocated, Pending: w.Pending}
		}
		if f := run.Status.Funding; f != nil {
			lenders := make([]string, 0, len(f.Lenders))
			for _, l := range f.Lenders {
				lenders = append(lenders, fmt.Sprintf("%s:%d", l.Owner, l.GPUs))
			}
			sort.Strings(lenders)
			gr.Funding = &goldenFunding{
				OwnedGPUs: f.OwnedGPUs, SharedGPUs: f.SharedGPUs,
				BorrowedGPUs: f.BorrowedGPUs, UnfundedGPUs: f.UnfundedGPUs, Lenders: lenders,
			}
		}
		snap.Runs = append(snap.Runs, gr)
	}
	sort.Slice(snap.Runs, func(i, j int) bool { return snap.Runs[i].Key < snap.Runs[j].Key })

	for i := range state.Leases {
		l := state.Leases[i]
		nodes := append([]string{}, l.Spec.Slice.Nodes...)
		sort.Strings(nodes)
		snap.Leases = append(snap.Leases, goldenLease{
			Owner: l.Spec.Owner, Run: l.Spec.RunRef.Namespace + "/" + l.Spec.RunRef.Name,
			PaidByBudget: l.Spec.PaidByBudget, PaidByEnvelope: l.Spec.PaidByEnvelope,
			CompPath: l.Spec.CompPath, GPUs: len(l.Spec.Slice.Nodes), Role: l.Spec.Slice.Role,
			Nodes: nodes, Reason: l.Spec.Reason, Closed: l.Status.Closed, ClosureReason: l.Status.ClosureReason,
		})
	}
	sort.Slice(snap.Leases, func(i, j int) bool { return leaseSortKey(snap.Leases[i]) < leaseSortKey(snap.Leases[j]) })

	for _, res := range state.Reservations {
		gr := goldenReservation{
			Name: res.Name, Run: res.Spec.RunRef.Namespace + "/" + res.Spec.RunRef.Name,
			State: res.Status.State, PayingEnvelope: res.Spec.PayingEnvelope,
			EarliestStartAfterNow: res.Spec.EarliestStart.Time.After(now),
		}
		if f := res.Status.Forecast; f != nil {
			gr.DeficitGPUs = f.DeficitGPUs
			gr.Confidence = f.Confidence
			gr.Remedies = append([]string{}, f.Remedies...)
		}
		snap.Reservations = append(snap.Reservations, gr)
	}
	sort.Slice(snap.Reservations, func(i, j int) bool { return snap.Reservations[i].Name < snap.Reservations[j].Name })

	for _, p := range state.Pods {
		snap.Pods = append(snap.Pods, goldenPod{
			Key: p.Namespace + "/" + p.Name, Run: p.Labels[binder.LabelRunName],
			Role: p.Labels[binder.LabelRunRole], Group: p.Labels[binder.LabelGroupIndex],
			GPUs: p.GPUs, NodeName: p.NodeName,
		})
	}
	sort.Slice(snap.Pods, func(i, j int) bool { return snap.Pods[i].Key < snap.Pods[j].Key })

	return snap
}

func leaseSortKey(l goldenLease) string {
	return strings.Join([]string{l.Run, l.Role, l.Reason, strings.Join(l.Nodes, ","), l.Owner, l.PaidByEnvelope}, "|")
}

func TestGoldenScenarios(t *testing.T) {
	scenarios := []struct {
		name string
		fn   func() (*ClusterState, time.Time)
	}{
		{"simple-fit", scnSimpleFit},
		{"borrow-sponsor-runs", scnBorrowSponsorRuns},
		{"borrow-limited-reservation", scnBorrowLimitedReservation},
		{"capacity-missing-reservation", scnCapacityMissingReservation},
		{"elastic-grow", scnElasticGrow},
		{"node-failure-swap", scnNodeFailureSwap},
	}
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			state, now := s.fn()
			got, err := json.MarshalIndent(snapshot(s.name, state, now), "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "golden", s.name+".json")
			if update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("updated %s", path)
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run UPDATE_GOLDEN=1 go test ./controllers/ -run TestGoldenScenarios to create)", path, err)
			}
			if string(got) != string(want) {
				t.Errorf("golden mismatch for %s (run UPDATE_GOLDEN=1 to accept an intended change):\n--- got ---\n%s\n--- want ---\n%s", s.name, got, want)
			}
		})
	}
}

package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// fakeRecorder is a minimal EventRecorder that records every call for
// assertion, standing in for the real client-go recorder the k8s bridge
// wires (proven separately by the envtest TestRunEmitsRealAdmittedEvent).
type fakeRecorder struct {
	events []recordedEvent
}

type recordedEvent struct {
	Run     string
	Type    string
	Reason  string
	Message string
}

func (f *fakeRecorder) Event(run *v1.Run, eventType, reason, message string) {
	name := "<nil>"
	if run != nil {
		name = run.Name
	}
	f.events = append(f.events, recordedEvent{Run: name, Type: eventType, Reason: reason, Message: message})
}

func (f *fakeRecorder) has(runName, eventType, reason string, messageContains string) bool {
	for _, e := range f.events {
		if e.Run != runName || e.Type != eventType || e.Reason != reason {
			continue
		}
		if messageContains == "" || strings.Contains(e.Message, messageContains) {
			return true
		}
	}
	return false
}

// TestEngineEmitsEventsIncludingAttestedSeed proves audit findings #9 (event
// streams) and #23 (attested lottery seed never logged) closed at the
// engine layer: varying the run's fate (bound, reserved-then-activated,
// resolver-shrunk, resolver-lottery-ended) produces distinctly reasoned
// events, and the lottery's Warning event carries the real seed string that
// closeLease recorded on the lease itself — not a separate, potentially
// inconsistent, copy.
func TestEngineEmitsEventsIncludingAttestedSeed(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	nodes := []topology.SourceNode{
		{Name: "node-a", GPUs: 8, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
		{Name: "node-b", GPUs: 8, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
		{Name: "node-c", GPUs: 8, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
	}
	budgets := []v1.Budget{
		{ObjectMeta: v1.ObjectMeta{Name: "owner-a"}, Spec: v1.BudgetSpec{Owner: "org:owner:a", Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB",
			Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
			Concurrency: 16,
		}}}},
		{ObjectMeta: v1.ObjectMeta{Name: "owner-b"}, Spec: v1.BudgetSpec{Owner: "org:owner:b", Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB",
			Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
			Concurrency: 16,
		}}}},
		{ObjectMeta: v1.ObjectMeta{Name: "owner-c"}, Spec: v1.BudgetSpec{Owner: "org:owner:c", Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB",
			Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
			Concurrency: 16,
		}}}},
	}

	state := &ClusterState{Nodes: nodes, Budgets: budgets}
	state.Runs = map[string]*v1.Run{
		"default/run-a": {
			ObjectMeta: v1.ObjectMeta{Name: "run-a", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:owner:a", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8}, Locality: &v1.RunLocality{GroupGPUs: int32Ptr(8)}},
		},
		"default/run-b": {
			ObjectMeta: v1.ObjectMeta{Name: "run-b", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:owner:b", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 16}, Locality: &v1.RunLocality{GroupGPUs: int32Ptr(8)}, Malleable: &v1.RunMalleability{MinTotalGPUs: 8, MaxTotalGPUs: 16, StepGPUs: 8}},
		},
	}

	rec := &fakeRecorder{}
	controller := NewRunController(state, runClock{now: now})
	controller.Recorder = rec

	// Bind path, post-cutover: Reconcile emits unscheduled intent pods and
	// leaves the run Pending — it mints nothing and no longer emits an
	// "Admitted" event (the scheduler plugin admits now). The bound "fate"
	// the resolver later shrinks/ends is delivered by the plugin, stood in for
	// by seedRunning below.
	if err := controller.Reconcile("default", "run-a"); err != nil {
		t.Fatalf("run-a reconcile failed: %v", err)
	}
	if err := controller.Reconcile("default", "run-b"); err != nil {
		t.Fatalf("run-b reconcile failed: %v", err)
	}
	if got := activeIntentPods(state, "default", "run-a"); got != 8 {
		t.Errorf("expected run-a to emit 8 unscheduled intent pods on the bind path, got %d (events %+v)", got, rec.events)
	}
	if got := activeIntentPods(state, "default", "run-b"); got != 16 {
		t.Errorf("expected run-b to emit 16 unscheduled intent pods on the bind path, got %d (events %+v)", got, rec.events)
	}
	if len(state.Leases) != 0 {
		t.Errorf("controller must mint nothing on the bind path, got %d leases", len(state.Leases))
	}
	if rec.has("run-a", EventTypeNormal, "Admitted", "") || rec.has("run-b", EventTypeNormal, "Admitted", "") {
		t.Errorf("engine must not emit an Admitted event on the bind path post-cutover, got %+v", rec.events)
	}
	// It emits the honest "Scheduling" event on the path it does own (requesting
	// width), once, when the intent pods are first created.
	if !rec.has("run-a", EventTypeNormal, "Scheduling", "") || !rec.has("run-b", EventTypeNormal, "Scheduling", "") {
		t.Errorf("engine must emit a Scheduling event when it emits intent pods, got %+v", rec.events)
	}

	// Stand in for the scheduler plugin scheduling + funding those intent pods:
	// run-a and run-b become Running with the exact leases admission.Plan mints
	// — the bound state the resolver later shrinks (run-b) and ends by lottery
	// (run-a) once run-c's reservation activates.
	seedRunning(t, state, "default/run-a", now)
	seedRunning(t, state, "default/run-b", now)

	state.Runs["default/run-c"] = &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "run-c", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:owner:c", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 16}, Locality: &v1.RunLocality{GroupGPUs: int32Ptr(8)}},
	}
	if err := controller.Reconcile("default", "run-c"); err != nil {
		t.Fatalf("run-c reconcile failed: %v", err)
	}
	if !rec.has("run-c", EventTypeNormal, "Reserved", "") {
		t.Errorf("expected a Reserved event for run-c, got %+v", rec.events)
	}

	var reservation *v1.Reservation
	for _, res := range state.Reservations {
		reservation = res
	}
	if reservation == nil {
		t.Fatalf("expected reservation for run-c")
	}

	activationTime := now.Add(30 * time.Minute)
	controller.Clock = runClock{now: activationTime}
	if err := controller.ActivateReservations(activationTime); err != nil {
		t.Fatalf("activate reservations failed: %v", err)
	}

	if !rec.has("run-c", EventTypeNormal, "Activated", "") {
		t.Errorf("expected an Activated event for run-c, got %+v", rec.events)
	}
	if !rec.has("run-b", EventTypeWarning, "ResolverShrink", "") {
		t.Errorf("expected a ResolverShrink event for run-b, got %+v", rec.events)
	}
	if !rec.has("run-a", EventTypeWarning, "ResolverEnded", "") {
		t.Errorf("expected a ResolverEnded event for run-a, got %+v", rec.events)
	}

	// The seed is embedded in the lottery's lease closure reason
	// ("RandomPreempt(0x...)") and must show up verbatim in a real Warning
	// event, not just a log line — this is the "discoverable via logs"
	// promise (audit finding #23), now discoverable via Events too.
	seedEvent := false
	for _, e := range rec.events {
		if e.Reason == "ResolverAction" && e.Type == EventTypeWarning && strings.Contains(e.Message, "RandomPreempt(0x") {
			seedEvent = true
			break
		}
	}
	if !seedEvent {
		t.Errorf("expected a ResolverAction event carrying the attested lottery seed, got %+v", rec.events)
	}
}

package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func followRun(name string, after ...string) *v1.Run {
	r := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: keys.DefaultNamespace, CreationTimestamp: v1.NewTime(qsBase)},
		Spec:       v1.RunSpec{Owner: "team", Resources: v1.RunResources{GPUType: qsFlavor, TotalGPUs: 4}},
	}
	if len(after) > 0 {
		r.Spec.Follow = &v1.RunFollow{After: after}
	}
	return r
}

// followWorld gives the follower enough capacity to bind once eligible.
func followWorld(runs ...*v1.Run) *ClusterState {
	state := &ClusterState{
		Nodes:        []topology.SourceNode{qsNode("n1", 8)},
		Budgets:      []v1.Budget{qsBudget("team-budget", "team", qsEnvelope("west", 8))},
		Runs:         map[string]*v1.Run{},
		Reservations: map[string]*v1.Reservation{},
	}
	for _, r := range runs {
		state.Runs[keys.NamespacedKey(r.Namespace, r.Name)] = r
	}
	return state
}

func TestFollowerWaitsUntilUpstreamCompletesThenAdmits(t *testing.T) {
	up := followRun("data-prep")
	up.Status.Phase = RunPhaseRunning
	down := followRun("train", "data-prep")
	state := followWorld(up, down)
	clock := &qsClock{now: qsBase}

	got := qsReconcile(t, state, clock, "train")
	if got.Status.Phase != RunPhaseWaiting {
		t.Fatalf("expected Waiting while upstream runs, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
	if !strings.Contains(got.Status.Message, "data-prep") {
		t.Errorf("waiting message should name the upstream, got %q", got.Status.Message)
	}

	// Upstream completes -> the follower clears the gate and admits.
	up.Status.Phase = RunPhaseComplete
	got = qsReconcile(t, state, clock, "train")
	if got.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected Running after upstream completed, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
}

func TestFollowerWaitsForAllUpstreams(t *testing.T) {
	a := followRun("a")
	a.Status.Phase = RunPhaseComplete
	b := followRun("b")
	b.Status.Phase = RunPhaseRunning
	down := followRun("eval", "a", "b")
	state := followWorld(a, b, down)
	clock := &qsClock{now: qsBase}

	got := qsReconcile(t, state, clock, "eval")
	if got.Status.Phase != RunPhaseWaiting || !strings.Contains(got.Status.Message, "b") {
		t.Fatalf("expected Waiting on b, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
	if strings.Contains(got.Status.Message, "waiting for: a") {
		t.Errorf("completed upstream a should not be listed, got %q", got.Status.Message)
	}
}

func TestUpstreamFailurePolicyFailFailsFollowerImmediately(t *testing.T) {
	up := followRun("data-prep")
	up.Status.Phase = RunPhaseFailed
	down := followRun("train", "data-prep")
	down.Spec.Follow.OnUpstreamFailure = v1.OnUpstreamFailureFail
	state := followWorld(up, down)

	got := qsReconcile(t, state, &qsClock{now: qsBase}, "train")
	if got.Status.Phase != RunPhaseFailed || !strings.Contains(got.Status.Message, "upstream failed") {
		t.Fatalf("policy fail should fail the follower, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
}

func TestUpstreamFailureWaitsThroughGraceThenFails(t *testing.T) {
	grace := metav1.Duration{Duration: time.Hour}
	up := followRun("data-prep")
	up.Status.Phase = RunPhaseFailed
	down := followRun("train", "data-prep")
	down.Spec.Follow.UpstreamFailureGrace = &grace // default policy is wait
	state := followWorld(up, down)
	clock := &qsClock{now: qsBase}

	// Within grace: Waiting with a deadline armed at base+1h.
	got := qsReconcile(t, state, clock, "train")
	if got.Status.Phase != RunPhaseWaiting {
		t.Fatalf("expected Waiting within grace, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
	if got.Status.FollowDeadline == nil || !got.Status.FollowDeadline.Time.Equal(qsBase.Add(time.Hour)) {
		t.Fatalf("expected deadline at base+1h, got %+v", got.Status.FollowDeadline)
	}

	// Still within grace at +30m: still Waiting.
	clock.now = qsBase.Add(30 * time.Minute)
	if got = qsReconcile(t, state, clock, "train"); got.Status.Phase != RunPhaseWaiting {
		t.Fatalf("expected still Waiting at +30m, got %s", got.Status.Phase)
	}

	// Past the deadline: the follower fails rather than zombie forever.
	clock.now = qsBase.Add(2 * time.Hour)
	got = qsReconcile(t, state, clock, "train")
	if got.Status.Phase != RunPhaseFailed || !strings.Contains(got.Status.Message, "grace expired") {
		t.Fatalf("expected Failed after grace expired, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
}

// A researcher fixing and resubmitting a failed upstream (so it completes)
// un-blocks a follower still within its grace window.
func TestUpstreamRecoversWithinGrace(t *testing.T) {
	up := followRun("data-prep")
	up.Status.Phase = RunPhaseFailed
	down := followRun("train", "data-prep") // default wait, default grace
	state := followWorld(up, down)
	clock := &qsClock{now: qsBase}

	if got := qsReconcile(t, state, clock, "train"); got.Status.Phase != RunPhaseWaiting {
		t.Fatalf("expected Waiting on the failed upstream, got %s", got.Status.Phase)
	}

	up.Status.Phase = RunPhaseComplete
	got := qsReconcile(t, state, clock, "train")
	if got.Status.Phase != RunPhaseRunning {
		t.Fatalf("a recovered upstream should let the follower admit, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
	if got.Status.FollowDeadline != nil {
		t.Errorf("deadline should clear once unblocked, got %+v", got.Status.FollowDeadline)
	}
}

func TestDeletedUpstreamBlocksFollower(t *testing.T) {
	down := followRun("train", "ghost")
	down.Spec.Follow.OnUpstreamFailure = v1.OnUpstreamFailureFail
	state := followWorld(down) // no "ghost" run exists

	got := qsReconcile(t, state, &qsClock{now: qsBase}, "train")
	if got.Status.Phase != RunPhaseFailed || !strings.Contains(got.Status.Message, "ghost") {
		t.Fatalf("a missing upstream (fail policy) should fail the follower, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
}

func TestFollowCycleFailsTheRun(t *testing.T) {
	a := followRun("a", "b")
	b := followRun("b", "a")
	state := followWorld(a, b)

	got := qsReconcile(t, state, &qsClock{now: qsBase}, "a")
	if got.Status.Phase != RunPhaseFailed || !strings.Contains(got.Status.Message, "cycle") {
		t.Fatalf("a follow cycle should fail the run, got %s (%s)", got.Status.Phase, got.Status.Message)
	}
}

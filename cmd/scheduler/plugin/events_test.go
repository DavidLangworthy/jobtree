package plugin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/pack"
)

// countingReader wraps a client.Reader and counts Gets, so a test can assert the Run
// UID is fetched once per gang and not once per Event on the funding hot path.
type countingReader struct {
	client client.Client
	gets   int
}

func (r *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.gets++
	return r.client.Get(ctx, key, obj, opts...)
}

func (r *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.client.List(ctx, list, opts...)
}

// fakeClientWith builds a client seeded with objs, using the plugin tests' scheme.
func fakeClientWith(objs ...client.Object) client.Client {
	sch := apiruntime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v1.AddToScheme(sch))
	return fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
}

// R20's whole point is that "not fundable" and "not placeable" are different answers.
// The distinction survives only if it is read off the ERROR TYPE — matching message
// text would break the first time a planner rephrased itself, and quietly, by
// classifying a capacity failure as a quota failure and sending a researcher to argue
// with their budget about a cluster that is simply full.
func TestARefusalIsClassifiedByTypeNotByMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want refusalKind
	}{
		{"pack: nothing fits", &pack.PlanError{Reason: pack.FailureReasonInsufficientCapacity, Msg: "no domain holds 8 GPUs"}, refusalUnplaceable},
		{"cover: no envelope", &cover.PlanError{Reason: cover.FailureReasonNoMatchingEnvelope, Msg: "no envelope for flavor H100-80GB"}, refusalUnfundable},
		{"cover: borrow limit", &cover.PlanError{Reason: cover.FailureReasonBorrowLimit, Msg: "borrow limit reached"}, refusalUnfundable},
		// A pack failure wrapped on its way up is still a pack failure. errors.As
		// unwraps; a type switch would not.
		{"wrapped pack error", fmt.Errorf("plan placement: %w", &pack.PlanError{Msg: "no capacity"}), refusalUnplaceable},
		// Anything else degrades to the pre-R20 answer rather than to a confident
		// wrong one.
		{"untyped error", fmt.Errorf("load world: connection refused"), refusalUnfundable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRefusal(tc.err); got != tc.want {
				t.Errorf("classifyRefusal(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The two kinds must produce two different Event reasons and two different
// explanations — if they rendered the same, preserving the type would be decorative.
func TestTheTwoRefusalsReadDifferently(t *testing.T) {
	if unfundableEvent(refusalUnplaceable) != ReasonGangUnplaceable {
		t.Errorf("an unplaceable gang reports %q", unfundableEvent(refusalUnplaceable))
	}
	if unfundableEvent(refusalUnfundable) != ReasonGangUnfundable {
		t.Errorf("an unfundable gang reports %q", unfundableEvent(refusalUnfundable))
	}
	placed := describeRefusal("default/train", refusalUnplaceable, "no domain holds 8 GPUs")
	funded := describeRefusal("default/train", refusalUnfundable, "no envelope covers 8")
	if placed == funded {
		t.Fatal("both refusals render identically; the distinction is decorative")
	}
	if !contains(placed, "not quota") {
		t.Errorf("the unplaceable message does not steer the reader away from their budget: %q", placed)
	}
	if !contains(funded, "quota") {
		t.Errorf("the unfundable message does not name quota: %q", funded)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// A gang of 64 pods, retried, would otherwise mirror hundreds of "still forming"
// Events onto one Run. The counts in the note change, so the Event API's own
// aggregation does not collapse them.
func TestGangFormingIsThrottledPerGang(t *testing.T) {
	now := time.Now()
	m := newGangManager(nil, func() time.Time { return now })

	if !m.shouldReportForming("default/train") {
		t.Fatal("the first observation of a gang must be reported")
	}
	for i := 0; i < 64; i++ {
		if m.shouldReportForming("default/train") {
			t.Fatalf("member %d reported again within the throttle window", i)
		}
	}
	// A different gang is a different answer and is never throttled by the first.
	if !m.shouldReportForming("default/other") {
		t.Fatal("a second gang was throttled by the first gang's report")
	}

	now = now.Add(formingEventInterval + time.Second)
	if !m.shouldReportForming("default/train") {
		t.Fatal("the throttle never expires; a long-forming gang would go silent")
	}
}

// GangTimeout is measured, not guessed. A member unreserved for any other reason —
// a sibling's rejection, a bind failure — must not be reported as a timeout, because
// that attributes a cause we do not know.
func TestOnlyAFullWaitCountsAsATimeout(t *testing.T) {
	now := time.Now()
	m := newGangManager(nil, func() time.Time { return now })

	if m.waitedOutTimeout("never-parked") {
		t.Error("a pod that never parked was reported as timed out")
	}

	m.noteWaiting("train-pod-0")
	if m.waitedOutTimeout("train-pod-0") {
		t.Error("a pod reported as timed out the instant it parked")
	}

	now = now.Add(permitTimeout - time.Second)
	if m.waitedOutTimeout("train-pod-0") {
		t.Error("a pod reported as timed out one second early")
	}

	now = now.Add(2 * time.Second)
	if !m.waitedOutTimeout("train-pod-0") {
		t.Error("a pod that waited the full permitTimeout was not reported as timed out")
	}

	// forget drops the stamp: the map is bounded by the pods currently in flight.
	m.forget(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "train-pod-0"}})
	if m.waitedOutTimeout("train-pod-0") {
		t.Error("the park stamp outlived the pod's trip through the gate")
	}
}

// The Run mirror needs the real UID (kubectl selects Events on it), so the cache is
// load-bearing rather than an optimisation: without it every mirrored Event would
// cost a Get on the funding hot path, and with a wrong UID it would be invisible.
func TestTheRunUIDIsFetchedOnceAndReused(t *testing.T) {
	run := &v1.Run{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "train", UID: "11111111-2222-3333-4444-555555555555",
	}}
	reader := &countingReader{client: fakeClientWith(run)}
	m := newGangManager(reader, time.Now)

	for i := 0; i < 10; i++ {
		if got := m.runUID("default", "train"); got != run.UID {
			t.Fatalf("runUID = %q, want %q", got, run.UID)
		}
	}
	if reader.gets != 1 {
		t.Errorf("the Run was fetched %d times for 10 Events; the cache is not working and this is the funding hot path", reader.gets)
	}
}

// A Run that cannot be read yields no UID and therefore no mirror. Narration must
// never fail a scheduling decision, and an Event on a made-up reference is worse than
// no Event: it exists in the API and is invisible in `kubectl describe`.
func TestAnUnreadableRunSilencesTheMirrorRatherThanGuessing(t *testing.T) {
	m := newGangManager(&countingReader{client: fakeClientWith()}, time.Now)
	if uid := m.runUID("default", "gone"); uid != "" {
		t.Errorf("runUID for a missing Run = %q, want empty", uid)
	}

	j := &JobTree{gm: m}
	if ref := j.runRefFor(&corev1.Pod{}); ref != nil {
		t.Error("a pod with no run label produced a Run reference")
	}
	// And the emission path itself is inert without a handle, which is how every
	// existing plugin unit test constructs the plugin.
	j.emit(&corev1.Pod{}, corev1.EventTypeNormal, ReasonGangForming, "Permit", "note")
	j.emitForming(&corev1.Pod{}, "default/train", 1, 0, 4)
}

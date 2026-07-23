package plugin

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// R20 — the plugin narrates its own decisions.
//
// The plugin is where placement and funding are actually decided, and it emitted no
// Events at all. A gang parked in Permit was invisible: the Run said `Pending`, the
// controller's Events said it had asked for GPUs, and the reason it was not starting
// existed only as a string in a `Wait` status that a researcher would have to know to
// go read off individual pods.
//
// Every Event below is emitted on the POD (where the framework's own scheduling events
// live, so the two read together) and mirrored onto the RUN (so `kubectl describe run`
// and `kubectl runs explain` answer "why isn't it starting?" without pod spelunking).
//
// This narrates; it decides nothing. No emission site changes a verdict, and the
// recorder is nil-safe so a plugin built without a handle (every unit test) behaves
// exactly as before.
const (
	// ReasonGangForming — members are still arriving. Normal: this is the healthy
	// state of a gang that is assembling, and it is also what a gang that is missing
	// one pod forever looks like, which is why it carries the count.
	ReasonGangForming = "GangForming"
	// ReasonGangUnfundable — the funding gate refused: no envelope covers this width.
	// This is the researcher's "you are out of quota" answer.
	ReasonGangUnfundable = "GangUnfundable"
	// ReasonGangUnplaceable — the PHYSICAL plan failed: the cluster cannot hold this
	// shape (capacity, topology), whatever the budget says.
	//
	// Distinct from Unfundable on purpose. `decide` collapsed both into one string and
	// Permit labelled every refusal "not fundable", which sends anyone debugging an
	// overcommitted cluster to look at budgets that are fine (funding-model review,
	// 2026-07-08).
	ReasonGangUnplaceable = "GangUnplaceable"
	// ReasonFlavorMismatch — the run asks for a GPU flavor no node in the cluster has.
	// Emitted from PostFilter, once per unschedulable pod, rather than from Filter,
	// which runs per NODE and would emit one Event per node per cycle.
	ReasonFlavorMismatch = "FlavorMismatch"
	// ReasonGangTimeout — a member sat at the gate for the full permitTimeout without
	// its gang assembling, and the framework rejected it. The gang re-forms.
	ReasonGangTimeout = "GangTimeout"
	// ReasonLeaseMinted — PreBind committed the lease. The positive audit signal: the
	// moment funding became a fact, with the envelope that pays for it.
	ReasonLeaseMinted = "LeaseMinted"
)

// runRefFor returns an object the EventRecorder can reference for this pod's Run.
//
// The UID matters and is not decoration. `kubectl describe run` searches Events by
// involvedObject.uid, so a reference carrying only name and namespace produces Events
// that exist in the API and are invisible in the one place a researcher looks. The
// gang manager caches the real UID (one Get per gang, never per pod — this is the hot
// path R4 exists to keep cheap) and returns nil when it does not have one, which makes
// the mirror silently skip rather than emit something undiscoverable.
func (j *JobTree) runRefFor(pod *corev1.Pod) *v1.Run {
	if j.gm == nil {
		return nil
	}
	ns, name := pod.Namespace, pod.Labels[binder.LabelRunName]
	if name == "" {
		return nil
	}
	uid := j.gm.runUID(ns, name)
	if uid == "" {
		return nil
	}
	return &v1.Run{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: uid}}
}

// emit records one Event on the pod and, when the Run's identity is known, mirrors it
// onto the Run. action is the framework's "what was being attempted" field.
func (j *JobTree) emit(pod *corev1.Pod, eventType, reason, action, note string, args ...interface{}) {
	if j.handle == nil {
		return
	}
	rec := j.handle.EventRecorder()
	if rec == nil {
		return
	}
	rec.Eventf(pod, nil, eventType, reason, action, note, args...)
	if run := j.runRefFor(pod); run != nil {
		rec.Eventf(run, pod, eventType, reason, action, note, args...)
	}
}

// formingEventInterval throttles the Run-visible GangForming mirror.
//
// Permit runs per member per attempt, so a 64-pod gang that re-forms a few times
// produces hundreds of identical "still forming" observations. The Event API
// aggregates repeats of the same (object, reason, note) into one object with a count,
// but the note here carries the CHANGING counts — which is the useful part and also
// defeats aggregation. Throttling per gang keeps the useful signal and the readable
// Event list.
const formingEventInterval = 30 * time.Second

// emitForming reports gang assembly progress, at most once per gang per interval.
func (j *JobTree) emitForming(pod *corev1.Pod, key string, waiting, committed, expected int) {
	if j.gm == nil || !j.gm.shouldReportForming(key) {
		return
	}
	j.emit(pod, corev1.EventTypeNormal, ReasonGangForming, "Permit",
		"gang %s is assembling: %d waiting + %d already committed of %d; it starts when the whole set is present",
		key, waiting, committed, expected)
}

// unfundableReason maps a refusal from admission.Feasible to its Event vocabulary.
// The typed distinction is preserved through decide() precisely so this mapping is a
// lookup and not a guess at the message text.
func unfundableEvent(kind refusalKind) string {
	if kind == refusalUnplaceable {
		return ReasonGangUnplaceable
	}
	return ReasonGangUnfundable
}

// describeRefusal renders the human half of an Unfundable/Unplaceable Event.
func describeRefusal(key string, kind refusalKind, reason string) string {
	switch kind {
	case refusalUnplaceable:
		return fmt.Sprintf("gang %s cannot be PLACED: %s. This is physical capacity or topology, not quota — a budget with headroom will not help.", key, reason)
	default:
		return fmt.Sprintf("gang %s cannot be FUNDED: %s. This is quota: no envelope covers the requested width.", key, reason)
	}
}

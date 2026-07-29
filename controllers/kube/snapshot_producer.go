package kube

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/snapshot"
)

// SnapshotProducer is the in-tree controller of DESIGN-v5 build item 3: it reads
// Budgets and Grants, validates each transition against the PRIOR ACCEPTED
// graph, compiles, and publishes the versioned document.
//
// It is the whole trust boundary (§11). No invariant the published document can
// express says "this changeset was authored by someone entitled to make it" —
// a forged edge and a legitimate one are the same shape once compiled — so
// authorisation is checked here, where writes are visible, and nowhere else.
// pkg/snapshot holds the pure compiler; this type is I/O and publication.
//
// Publication is deliberately boring: one cluster-scoped document, recompiled on
// a tick, written only when the content hash moves. Sharding is not built
// (Ruling 18); revisit above ~2,000 principals.
type SnapshotProducer struct {
	Client    client.Client
	APIReader client.Reader
	Clock     controllers.Clock
	Recorder  record.EventRecorder
	// Interval is how often the graph is recompiled.
	Interval time.Duration
}

func (p *SnapshotProducer) defaults() {
	if p.Interval <= 0 {
		p.Interval = 30 * time.Second
	}
	if p.Clock == nil {
		p.Clock = controllers.RealClock{}
	}
}

// SetupWithManager registers the producer as a Runnable.
func (p *SnapshotProducer) SetupWithManager(mgr ctrl.Manager) error {
	p.defaults()
	return mgr.Add(p)
}

// Start recompiles on a ticker until the context ends.
func (p *SnapshotProducer) Start(ctx context.Context) error {
	p.defaults()
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		if err := p.Publish(ctx); err != nil {
			log.FromContext(ctx).Error(err, "snapshot producer could not publish")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Publish runs one compile-and-publish pass. Exposed so envtest can drive a
// deterministic single pass instead of waiting on the ticker.
func (p *SnapshotProducer) Publish(ctx context.Context) error {
	p.defaults()

	var budgets v1.BudgetList
	if err := p.APIReader.List(ctx, &budgets); err != nil {
		return fmt.Errorf("list budgets: %w", err)
	}
	var grants v1.GrantList
	if err := p.APIReader.List(ctx, &grants); err != nil {
		return fmt.Errorf("list grants: %w", err)
	}
	var namespaces corev1.NamespaceList
	if err := p.APIReader.List(ctx, &namespaces); err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}
	uids := make(map[string]string, len(namespaces.Items))
	for i := range namespaces.Items {
		ns := &namespaces.Items[i]
		uids[ns.Name] = string(ns.UID)
	}

	// The PRIOR ACCEPTED graph, not the aggregate candidate document (§4.1).
	// Validating transitions against the accepted graph is what makes quarantine
	// guilt-scoped: the incumbent wins, and only the causative new or changed
	// object is refused.
	prior := &v1.QuotaSnapshot{}
	if err := p.APIReader.Get(ctx, client.ObjectKey{Name: snapshot.SnapshotName}, prior); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("read prior snapshot: %w", err)
		}
		prior = nil
	}

	res, err := snapshot.Compile(snapshot.Input{
		Budgets:       budgets.Items,
		Grants:        grants.Items,
		NamespaceUIDs: uids,
		Prior:         prior,
		Now:           p.Clock.Now(),
	})
	if err != nil {
		// A compile error means identity itself is ambiguous, so there is no
		// document to publish. FAIL CLOSED: keep the last accepted snapshot
		// rather than publishing a graph we cannot vouch for. Cold start funds
		// nothing, which is safe because no funded demand produces no eviction
		// (§8, Ruling 4).
		p.alarmCompileFailure(ctx, err)
		return err
	}

	p.reportRefusals(ctx, res)
	p.syncGrantStatus(ctx, grants.Items, res)

	return p.write(ctx, prior, res)
}

// write creates or updates the single published document.
//
// INV-SNAP-IMMUTABLE says a published snapshot never changes meaning. That holds
// through the VERSION rather than through object identity: an update carries a
// new snapshotVersion and a new effectiveFrom, and readers pin the version they
// resolved against (that is what Lease.Spec.SnapshotVersion records). Keeping
// one object rather than accumulating one per version is Ruling 18's "one
// document" applied to the API surface too.
func (p *SnapshotProducer) write(ctx context.Context, prior *v1.QuotaSnapshot, res snapshot.Result) error {
	// REFUSED OBJECTS ARE NOT IN THE DOCUMENT, so the content hash cannot
	// reflect them: a quarantined Grant is precisely one that did not compile
	// in. Status therefore has to be reconciled on EVERY pass, independently of
	// whether the spec moved. Skipping it when the hash is unchanged leaves the
	// quarantine count frozen at whatever the first pass happened to see — and a
	// stale zero is exactly the silent loss of authority §4 forbids.
	quarantined := int32(len(res.Quarantined) + len(res.Rejected))
	principals := int32(len(res.Snapshot.Spec.Principals))
	if prior != nil && prior.Spec.ContentHash == res.Snapshot.Spec.ContentHash {
		// Nothing changed: do not burn a version or rewrite the spec, but do
		// keep the counts honest.
		return p.syncStatus(ctx, principals, quarantined)
	}
	if prior == nil {
		doc := res.Snapshot
		doc.Status = v1.QuotaSnapshotStatus{}
		if err := p.Client.Create(ctx, &doc); err != nil {
			return fmt.Errorf("create snapshot: %w", err)
		}
		return p.syncStatus(ctx, principals, quarantined)
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var live v1.QuotaSnapshot
		if err := p.Client.Get(ctx, client.ObjectKey{Name: snapshot.SnapshotName}, &live); err != nil {
			return err
		}
		live.Spec = res.Snapshot.Spec
		return p.Client.Update(ctx, &live)
	}); err != nil {
		return err
	}
	return p.syncStatus(ctx, principals, quarantined)
}

// syncStatus writes the at-a-glance counts, re-reading first so it never races
// the spec write above.
func (p *SnapshotProducer) syncStatus(ctx context.Context, principals, quarantined int32) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var live v1.QuotaSnapshot
		if err := p.Client.Get(ctx, client.ObjectKey{Name: snapshot.SnapshotName}, &live); err != nil {
			return err
		}
		if live.Status.PrincipalCount == principals && live.Status.QuarantinedCount == quarantined {
			return nil
		}
		live.Status.PrincipalCount = principals
		live.Status.QuarantinedCount = quarantined
		return p.Client.Status().Update(ctx, &live)
	})
}

// syncGrantStatus records, on each Grant, whether THIS revision compiled in.
//
// This is the reporting half of REVISION-GRANULAR quarantine (§4.3). v4 said
// both "retain the previously accepted binding" and "a quarantined Grant
// authorises nothing", which contradict each other for an invalid UPDATE to an
// already-accepted Grant. v5 resolves it: the ACCEPTED REVISION stays
// authoritative and only the candidate revision is credit-free. So
// acceptedRevision must keep pointing at the revision that actually compiled in
// — NOT at the candidate that just failed — or an operator reading the object
// cannot tell which of the two is currently paying for work.
func (p *SnapshotProducer) syncGrantStatus(ctx context.Context, grants []v1.Grant, res snapshot.Result) {
	acceptedRev := map[string]string{}
	for _, pr := range res.Snapshot.Spec.Principals {
		if g := pr.InboundGrant; g != nil {
			acceptedRev[g.Namespace+"/"+g.Name] = g.Revision
		}
	}
	for i := range grants {
		g := &grants[i]
		key := g.Namespace + "/" + g.Name
		reason, quarantined := res.Quarantined[key]
		if !quarantined {
			reason, quarantined = res.Rejected[key]
		}

		desired := g.Status.DeepCopy()
		desired.SnapshotVersion = res.Snapshot.Spec.SnapshotVersion
		if rev, ok := acceptedRev[key]; ok {
			desired.AcceptedRevision = rev
		}
		// On a refusal, acceptedRevision is deliberately LEFT ALONE: the
		// previously accepted revision remains the authoritative one.
		cond := metav1.Condition{
			Type:               "Accepted",
			Status:             metav1.ConditionTrue,
			Reason:             "Compiled",
			Message:            fmt.Sprintf("compiled into snapshot %s", res.Snapshot.Spec.SnapshotVersion),
			LastTransitionTime: metav1.NewTime(p.Clock.Now()),
		}
		if quarantined {
			cond.Status = metav1.ConditionFalse
			cond.Reason = "NotCompiled"
			cond.Message = reason
		}
		meta.SetStatusCondition(&desired.Conditions, cond)

		if equalGrantStatus(&g.Status, desired) {
			continue
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var live v1.Grant
			if err := p.Client.Get(ctx, client.ObjectKey{Namespace: g.Namespace, Name: g.Name}, &live); err != nil {
				return err
			}
			live.Status.SnapshotVersion = desired.SnapshotVersion
			live.Status.AcceptedRevision = desired.AcceptedRevision
			meta.SetStatusCondition(&live.Status.Conditions, cond)
			return p.Client.Status().Update(ctx, &live)
		}); err != nil {
			log.FromContext(ctx).Error(err, "could not record grant status", "grant", key)
		}
	}
}

func equalGrantStatus(a, b *v1.GrantStatus) bool {
	if a.SnapshotVersion != b.SnapshotVersion || a.AcceptedRevision != b.AcceptedRevision {
		return false
	}
	ac := meta.FindStatusCondition(a.Conditions, "Accepted")
	bc := meta.FindStatusCondition(b.Conditions, "Accepted")
	if ac == nil || bc == nil {
		return ac == bc
	}
	return ac.Status == bc.Status && ac.Reason == bc.Reason && ac.Message == bc.Message
}

// reportRefusals makes every refusal LOUD.
//
// §4: "a silent quarantine is a silent loss of authority." A refused Grant is a
// lead discovering their delegation simply did not happen, with no signal — the
// exact failure mode the R26 wiring exists to end, in a second place. Every
// refusal names the object and the reason on the object itself.
func (p *SnapshotProducer) reportRefusals(ctx context.Context, res snapshot.Result) {
	report := func(kind string, m map[string]string) {
		for key, reason := range m {
			log.FromContext(ctx).Error(nil, "grant did not compile into the snapshot",
				"grant", key, "disposition", kind, "reason", reason)
			if p.Recorder == nil {
				continue
			}
			ns, name := splitKey(key)
			p.Recorder.Eventf(
				&v1.Grant{ObjectMeta: v1.ObjectMeta{Namespace: ns, Name: name}},
				corev1.EventTypeWarning, "GrantNotCompiled",
				"%s: %s", kind, reason)
		}
	}
	report("Quarantined", res.Quarantined)
	report("Rejected", res.Rejected)
}

func (p *SnapshotProducer) alarmCompileFailure(ctx context.Context, err error) {
	log.FromContext(ctx).Error(err,
		"identity graph is ambiguous; keeping the last accepted snapshot and publishing nothing")
}

func splitKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

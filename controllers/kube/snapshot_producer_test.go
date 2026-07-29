package kube

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/snapshot"
)

// resetProducerWorld clears the graph the producer compiles from. resetWorld
// only cleans the `default` namespace, and these tests deliberately span several
// namespaces (identity IS namespace binding), so without this the second test
// would compile the first test's principals and disagree about the graph.
func resetProducerWorld(t *testing.T) {
	t.Helper()
	for _, ns := range []string{"prod-lead", "prod-team", "prod-outsider", "stable-lead"} {
		if err := kubeClient.DeleteAllOf(suiteCtx, &v1.Budget{}, client.InNamespace(ns)); err != nil {
			t.Fatalf("clear budgets in %s: %v", ns, err)
		}
		if err := kubeClient.DeleteAllOf(suiteCtx, &v1.Grant{}, client.InNamespace(ns)); err != nil {
			t.Fatalf("clear grants in %s: %v", ns, err)
		}
	}
	var doc v1.QuotaSnapshot
	if err := kubeClient.Get(suiteCtx, client.ObjectKey{Name: snapshot.SnapshotName}, &doc); err == nil {
		if err := kubeClient.Delete(suiteCtx, &doc); err != nil {
			t.Fatalf("delete snapshot: %v", err)
		}
	}
	eventually(t, 10*time.Second, func() error {
		var budgets v1.BudgetList
		if err := kubeClient.List(suiteCtx, &budgets); err != nil {
			return err
		}
		for i := range budgets.Items {
			if budgets.Items[i].Namespace != "default" {
				return errNotClosed
			}
		}
		return nil
	})
}

func producerNamespace(t *testing.T, name string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := kubeClient.Create(suiteCtx, ns); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create namespace %s: %v", name, err)
	}
	// kubeClient is cached, so a freshly created namespace can lag. Poll rather
	// than assume read-your-write.
	var got corev1.Namespace
	eventually(t, 15*time.Second, func() error {
		return kubeClient.Get(suiteCtx, client.ObjectKey{Name: name}, &got)
	})
	return string(got.UID)
}

func producerBudget(t *testing.T, ns, budgetName, owner string, concurrency int32) {
	t.Helper()
	start := metav1.NewTime(baseTime.Add(-24 * time.Hour))
	end := metav1.NewTime(baseTime.Add(720 * time.Hour))
	b := &v1.Budget{
		ObjectMeta: metav1.ObjectMeta{Name: budgetName, Namespace: ns},
		Spec: v1.BudgetSpec{
			Owner: owner,
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "west",
				Flavor:      "H100-80GB",
				Selector:    map[string]string{"region": "us-west"},
				Concurrency: concurrency,
				Start:       &start,
				End:         &end,
			}},
		},
	}
	if err := kubeClient.Create(suiteCtx, b); err != nil {
		t.Fatalf("create budget in %s: %v", ns, err)
	}
}

// TestProducerPublishesAndRefusesAnUnauthorizedGrant is the producer specimen
// running against a REAL API SERVER (DESIGN-v5 build items 3 and 11).
//
// The pure compiler is covered exhaustively in pkg/snapshot; what only envtest
// can prove is that the CRDs the producer needs actually exist, that a
// well-formed Grant is ACCEPTED by the apiserver (so the refusal below is the
// producer's judgement and not a schema rejection), and that the compiled
// document round-trips through the API.
//
// The attack is the one §11 names: a namespace bound to no principal writes a
// perfectly-shaped Grant handing itself authority. No document invariant can
// catch it — a forged edge and a real one compile to the same shape — so the
// producer has to, and it must refuse on WHERE the Grant was written.
func TestProducerPublishesAndRefusesAnUnauthorizedGrant(t *testing.T) {
	requireEnv(t)
	resetWorld(t)
	resetProducerWorld(t)

	producerNamespace(t, "prod-lead")
	producerNamespace(t, "prod-team")
	producerNamespace(t, "prod-outsider")

	producerBudget(t, "prod-lead", "lead-budget", "org:lead", 64)
	producerBudget(t, "prod-team", "team-budget", "org:team", 32)

	start := metav1.NewTime(baseTime.Add(-24 * time.Hour))
	end := metav1.NewTime(baseTime.Add(720 * time.Hour))
	forged := &v1.Grant{
		ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: "prod-outsider"},
		Spec: v1.GrantSpec{
			GranteeOwner:     "org:team",
			GranteeNamespace: "prod-team",
			Caps:             []v1.GrantCap{{Flavor: "H100-80GB", MaxConcurrency: 1000}},
			Start:            &start,
			End:              &end,
		},
	}
	// The apiserver ACCEPTS it — it is a well-formed object. That is the point:
	// authorisation is not a schema property.
	if err := kubeClient.Create(suiteCtx, forged); err != nil {
		t.Fatalf("the forged grant should be schema-valid; the producer is what refuses it: %v", err)
	}

	rec := record.NewFakeRecorder(32)
	p := &SnapshotProducer{
		Client:    kubeClient,
		APIReader: kubeClient,
		Clock:     &testClock{now: baseTime},
		Recorder:  rec,
	}

	eventually(t, 20*time.Second, func() error {
		if err := p.Publish(suiteCtx); err != nil {
			return err
		}
		var doc v1.QuotaSnapshot
		if err := kubeClient.Get(suiteCtx, client.ObjectKey{Name: snapshot.SnapshotName}, &doc); err != nil {
			return err
		}
		if len(doc.Spec.Principals) < 2 {
			return errNotClosed
		}
		// Wait for the forged Grant to be VISIBLE too: the cached lister can
		// lag, and a pass that has not seen it yet would read as a clean graph.
		if doc.Status.QuarantinedCount < 1 {
			return errNotClosed
		}
		return nil
	})

	var doc v1.QuotaSnapshot
	if err := kubeClient.Get(suiteCtx, client.ObjectKey{Name: snapshot.SnapshotName}, &doc); err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if doc.Spec.SchemaVersion != v1.QuotaSnapshotSchemaVersion {
		t.Errorf("schemaVersion = %q, want %q", doc.Spec.SchemaVersion, v1.QuotaSnapshotSchemaVersion)
	}
	if doc.Spec.SnapshotVersion == "" || doc.Spec.ContentHash == "" || doc.Spec.EffectiveFrom.IsZero() {
		t.Errorf("published document is missing its identity fields: %+v", doc.Spec)
	}

	var team *v1.SnapshotPrincipal
	for i := range doc.Spec.Principals {
		if doc.Spec.Principals[i].Owner == "org:team" {
			team = &doc.Spec.Principals[i]
		}
	}
	if team == nil {
		t.Fatalf("org:team absent from the published document: %+v", doc.Spec.Principals)
	}
	// The forged grant authorised nothing: no inbound edge, and the victim keeps
	// its own 32 rather than the forged 1000.
	if team.InboundGrant != nil {
		t.Errorf("a grant from an unbound namespace became authority: %+v", team.InboundGrant)
	}
	if len(team.Envelopes) != 1 || team.Envelopes[0].Concurrency != 32 {
		t.Errorf("victim's allocation = %+v, want its own 32", team.Envelopes)
	}
	// Identity is keyed by namespace UID, and the document says so.
	if team.BoundNamespace.UID == "" || team.BoundNamespace.Name != "prod-team" {
		t.Errorf("boundNamespace = %+v, want prod-team with a real UID", team.BoundNamespace)
	}
	if doc.Status.QuarantinedCount < 1 {
		t.Errorf("quarantinedCount = %d, want the refusal counted", doc.Status.QuarantinedCount)
	}

	// A silent quarantine is a silent loss of authority — the refusal is loud.
	var sawEvent bool
	for len(rec.Events) > 0 {
		if e := <-rec.Events; strings.Contains(e, "GrantNotCompiled") {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Error("the refused Grant produced no Event; a silent refusal is a silent loss of authority")
	}
}

// A no-op recompile must not burn a version: "the published version changed"
// has to mean "something actually changed", because Lease.Spec.SnapshotVersion
// pins against it.
func TestProducerNoOpRecompileKeepsTheVersion(t *testing.T) {
	requireEnv(t)
	resetWorld(t)
	resetProducerWorld(t)

	producerNamespace(t, "stable-lead")
	producerBudget(t, "stable-lead", "stable-budget", "org:stable", 16)

	p := &SnapshotProducer{
		Client:    kubeClient,
		APIReader: kubeClient,
		Clock:     &testClock{now: baseTime},
	}
	eventually(t, 20*time.Second, func() error {
		if err := p.Publish(suiteCtx); err != nil {
			return err
		}
		var doc v1.QuotaSnapshot
		if err := kubeClient.Get(suiteCtx, client.ObjectKey{Name: snapshot.SnapshotName}, &doc); err != nil {
			return err
		}
		if len(doc.Spec.Principals) == 0 {
			return errNotClosed
		}
		return nil
	})

	var first v1.QuotaSnapshot
	if err := kubeClient.Get(suiteCtx, client.ObjectKey{Name: snapshot.SnapshotName}, &first); err != nil {
		t.Fatalf("get snapshot: %v", err)
	}

	// Recompile at a LATER instant with no content change.
	p.Clock = &testClock{now: baseTime.Add(time.Hour)}
	if err := p.Publish(suiteCtx); err != nil {
		t.Fatalf("recompile: %v", err)
	}
	var second v1.QuotaSnapshot
	if err := kubeClient.Get(suiteCtx, client.ObjectKey{Name: snapshot.SnapshotName}, &second); err != nil {
		t.Fatalf("get snapshot again: %v", err)
	}
	if first.Spec.SnapshotVersion != second.Spec.SnapshotVersion {
		t.Errorf("a no-op recompile burned a version: %q -> %q",
			first.Spec.SnapshotVersion, second.Spec.SnapshotVersion)
	}
	if first.Spec.ContentHash != second.Spec.ContentHash {
		t.Errorf("content hash moved with no content change: %q -> %q",
			first.Spec.ContentHash, second.Spec.ContentHash)
	}

	// A real change must advance it.
	var b v1.Budget
	if err := kubeClient.Get(suiteCtx, client.ObjectKey{Namespace: "stable-lead", Name: "stable-budget"}, &b); err != nil {
		t.Fatalf("get budget: %v", err)
	}
	b.Spec.Envelopes[0].Concurrency = 24
	if err := kubeClient.Update(suiteCtx, &b); err != nil {
		t.Fatalf("update budget: %v", err)
	}
	eventually(t, 20*time.Second, func() error {
		if err := p.Publish(suiteCtx); err != nil {
			return err
		}
		var doc v1.QuotaSnapshot
		if err := kubeClient.Get(suiteCtx, client.ObjectKey{Name: snapshot.SnapshotName}, &doc); err != nil {
			return err
		}
		if doc.Spec.SnapshotVersion == first.Spec.SnapshotVersion {
			return errNotClosed
		}
		return nil
	})
}

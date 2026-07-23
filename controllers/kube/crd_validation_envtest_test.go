package kube

import (
	"fmt"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// R13 + R14 verification, against a real apiserver.
//
// Both items are claims about what the API SERVER does, and neither can be
// checked anywhere else. R13 says our lease kind no longer collides with
// coordination.k8s.io/Lease — that is a question about discovery, not about Go
// types, and a rename that missed the CRD or the RBAC would still compile.
// R14 says the field and immutability invariants hold WITHOUT the webhook: the
// only honest way to test that is to take the webhook away and try.

// directClient talks to the apiserver without the manager's cache. Every
// assertion here is about the write path's verdict, and a cached read after a
// rejected write is meaningless.
func directClient(t *testing.T) client.Client {
	t.Helper()
	c, err := client.New(restCfg, client.Options{Scheme: kubeClient.Scheme()})
	if err != nil {
		t.Fatalf("direct client: %v", err)
	}
	return c
}

// TestTheLeaseKindNoLongerCollidesWithCoordination is R13's invariant, asked of
// the apiserver's own discovery: `gpuleases` is ours, `leases` is core, and they
// are two different resources in two different groups. Before the rename
// `kubectl get leases` was ambiguous between them, and an RBAC rule naming
// `leases` could grant either one.
func TestTheLeaseKindNoLongerCollidesWithCoordination(t *testing.T) {
	requireEnv(t)

	dc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		t.Fatalf("discovery client: %v", err)
	}

	ours, err := dc.ServerResourcesForGroupVersion("rq.davidlangworthy.io/v1")
	if err != nil {
		t.Fatalf("discover rq.davidlangworthy.io/v1: %v", err)
	}
	var names []string
	for _, r := range ours.APIResources {
		names = append(names, r.Name)
	}
	found := false
	for _, r := range ours.APIResources {
		if r.Name == "gpuleases" {
			found = true
			if r.Kind != "GPULease" {
				t.Errorf("gpuleases has kind %q, want GPULease", r.Kind)
			}
			if !containsString(r.ShortNames, "gl") {
				t.Errorf("gpuleases short names = %v, want to include gl", r.ShortNames)
			}
		}
		// The whole point of R13: nothing of ours may be called `leases`.
		if r.Name == "leases" {
			t.Errorf("rq.davidlangworthy.io still serves %q — that is the collision R13 removed", r.Name)
		}
	}
	if !found {
		t.Fatalf("rq.davidlangworthy.io/v1 does not serve gpuleases; it serves %v", names)
	}

	// And the core one is untouched. If a future "cleanup" renames this back,
	// or an RBAC rule is written against the wrong group, this is the tell.
	core, err := dc.ServerResourcesForGroupVersion("coordination.k8s.io/v1")
	if err != nil {
		t.Fatalf("discover coordination.k8s.io/v1: %v", err)
	}
	coreLeases := false
	for _, r := range core.APIResources {
		if r.Name == "leases" && r.Kind == "Lease" {
			coreLeases = true
		}
	}
	if !coreLeases {
		t.Fatalf("coordination.k8s.io/v1 does not serve leases; the collision premise no longer holds and R13's rationale needs re-checking")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// withoutTheWebhook deletes the ValidatingWebhookConfiguration for the duration
// of the test and puts it back afterwards. This is R14's entire point: the
// webhook is failurePolicy=Fail, so "the webhook is having a bad day" means an
// operator either loses all writes or flips it to Ignore and loses all
// validation. What survives that is what is actually enforced.
func withoutTheWebhook(t *testing.T) {
	t.Helper()
	c := directClient(t)

	var list admissionv1.ValidatingWebhookConfigurationList
	if err := c.List(suiteCtx, &list); err != nil {
		t.Fatalf("list validating webhook configurations: %v", err)
	}
	var saved []admissionv1.ValidatingWebhookConfiguration
	for i := range list.Items {
		cfg := list.Items[i]
		if !mentionsOurGroup(cfg) {
			continue
		}
		saved = append(saved, cfg)
		if err := c.Delete(suiteCtx, &list.Items[i]); err != nil {
			t.Fatalf("delete webhook config %s: %v", cfg.Name, err)
		}
	}
	if len(saved) == 0 {
		t.Fatal("no jobtree ValidatingWebhookConfiguration is installed; this test would prove nothing")
	}

	t.Cleanup(func() {
		for i := range saved {
			cfg := saved[i].DeepCopy()
			cfg.ResourceVersion = ""
			cfg.UID = ""
			if err := c.Create(suiteCtx, cfg); err != nil && !apierrors.IsAlreadyExists(err) {
				// Loud: every later test in this package runs with validation
				// missing if this fails, and they would pass for the wrong reason.
				t.Errorf("FAILED TO RESTORE webhook config %s: %v", cfg.Name, err)
			}
		}
		// The webhook config is read per-request by the apiserver, but give the
		// manager's own caches a beat to see it again before the next test.
		eventually(t, 10*time.Second, func() error {
			var back admissionv1.ValidatingWebhookConfigurationList
			if err := c.List(suiteCtx, &back); err != nil {
				return err
			}
			for i := range back.Items {
				if mentionsOurGroup(back.Items[i]) {
					return nil
				}
			}
			return fmt.Errorf("webhook configuration not restored yet")
		})
	})
}

func mentionsOurGroup(cfg admissionv1.ValidatingWebhookConfiguration) bool {
	for _, wh := range cfg.Webhooks {
		for _, rule := range wh.Rules {
			for _, g := range rule.APIGroups {
				if g == "rq.davidlangworthy.io" {
					return true
				}
			}
		}
	}
	return false
}

// The R14 headline: with no webhook at all, the apiserver still refuses the
// field-level nonsense the webhook used to be the only thing catching — and
// still accepts a valid object, which is the half that makes the check
// falsifiable.
func TestTheAPIServerValidatesRunsWithTheWebhookDown(t *testing.T) {
	requireEnv(t)
	resetWorld(t)
	withoutTheWebhook(t)
	c := directClient(t)

	valid := func() *v1.Run {
		return &v1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "schema-probe", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:team",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
			},
		}
	}

	rejected := []struct {
		name string
		want string
		mut  func(*v1.Run)
	}{
		{"totalGPUs=0", "totalGPUs", func(r *v1.Run) { r.Spec.Resources.TotalGPUs = 0 }},
		{"empty gpuType", "gpuType", func(r *v1.Run) { r.Spec.Resources.GPUType = "" }},
		{"empty owner", "owner", func(r *v1.Run) { r.Spec.Owner = "" }},
		{"negative spares", "sparesPerGroup", func(r *v1.Run) { n := int32(-1); r.Spec.Spares = &n }},
		{"malleable min > max", "minTotalGPUs", func(r *v1.Run) {
			r.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 16, MaxTotalGPUs: 8, StepGPUs: 8}
		}},
		{"totalGPUs outside malleable range", "malleable min/max", func(r *v1.Run) {
			r.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 16, MaxTotalGPUs: 32, StepGPUs: 8}
		}},
		{"totalGPUs off the step grid", "stepGPUs", func(r *v1.Run) {
			r.Spec.Resources.TotalGPUs = 9
			r.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 8, MaxTotalGPUs: 32, StepGPUs: 8}
		}},
		{"role width 0", "width", func(r *v1.Run) {
			r.Spec.Roles = []v1.RunRole{{Name: "trainer", Width: 0, GPUsPerPod: 8}}
		}},
		{"role name is not a DNS label", "name", func(r *v1.Run) {
			r.Spec.Roles = []v1.RunRole{{Name: "Trainer_1", Width: 1, GPUsPerPod: 8}}
		}},
	}

	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			run := valid()
			tc.mut(run)
			err := c.Create(suiteCtx, run)
			if err == nil {
				_ = c.Delete(suiteCtx, run)
				t.Fatalf("apiserver ACCEPTED %s with the webhook down; this invariant is webhook-only, which is exactly what R14 removes", tc.name)
			}
			if !apierrors.IsInvalid(err) {
				t.Fatalf("rejected %s with %v, want an Invalid (schema/CEL) error", tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("rejection of %s does not mention %q: %v", tc.name, tc.want, err)
			}
		})
	}

	t.Run("a valid run is still accepted", func(t *testing.T) {
		run := valid()
		if err := c.Create(suiteCtx, run); err != nil {
			t.Fatalf("apiserver rejected a VALID run: %v", err)
		}
		if err := c.Delete(suiteCtx, run); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})
}

// A lease is a funding fact. R14 moves its immutability into the apiserver so a
// webhook outage cannot make spend re-attributable.
func TestTheAPIServerEnforcesLeaseImmutabilityWithTheWebhookDown(t *testing.T) {
	requireEnv(t)
	resetWorld(t)
	withoutTheWebhook(t)
	c := directClient(t)

	lease := &v1.GPULease{
		ObjectMeta: metav1.ObjectMeta{Name: "immutable-fact", Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:team",
			RunRef:         v1.RunReference{Name: "whoever", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a#0"}, Role: binder.RoleActive},
			Interval:       v1.GPULeaseInterval{Start: metav1.NewTime(baseTime)},
			PaidByEnvelope: "west-h100",
			Reason:         "Start",
		},
	}
	if err := c.Create(suiteCtx, lease); err != nil {
		t.Fatalf("create lease: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(suiteCtx, lease) })

	t.Run("the payer cannot be rewritten", func(t *testing.T) {
		var got v1.GPULease
		if err := c.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "immutable-fact"}, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		got.Spec.PaidByEnvelope = "somebody-elses-envelope"
		err := c.Update(suiteCtx, &got)
		if err == nil {
			t.Fatal("apiserver ACCEPTED a rewritten payer on a settled lease")
		}
		if !apierrors.IsInvalid(err) {
			t.Fatalf("update failed with %v, want an Invalid (CEL) error", err)
		}
	})

	t.Run("closing is allowed and reopening is not", func(t *testing.T) {
		var got v1.GPULease
		if err := c.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "immutable-fact"}, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		ended := metav1.NewTime(baseTime.Add(time.Hour))
		got.Status.Closed = true
		got.Status.Ended = &ended
		got.Status.ClosureReason = "Completed"
		if err := c.Status().Update(suiteCtx, &got); err != nil {
			t.Fatalf("apiserver rejected a legitimate closure: %v", err)
		}

		var closed v1.GPULease
		if err := c.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "immutable-fact"}, &closed); err != nil {
			t.Fatalf("get closed: %v", err)
		}
		closed.Status.Closed = false
		err := c.Status().Update(suiteCtx, &closed)
		if err == nil {
			t.Fatal("apiserver ACCEPTED reopening a closed lease; a settled interval would start billing again from its original start")
		}
		if !apierrors.IsInvalid(err) {
			t.Fatalf("reopen failed with %v, want an Invalid (CEL) error", err)
		}
	})

	t.Run("a lease with no slot is refused", func(t *testing.T) {
		bad := lease.DeepCopy()
		bad.ObjectMeta = metav1.ObjectMeta{Name: "holds-nothing", Namespace: "default"}
		bad.Spec.Slice.Nodes = nil
		if err := c.Create(suiteCtx, bad); err == nil {
			_ = c.Delete(suiteCtx, bad)
			t.Fatal("apiserver ACCEPTED a lease holding no slot")
		} else if !apierrors.IsInvalid(err) {
			t.Fatalf("rejected with %v, want Invalid", err)
		}
	})

	t.Run("a lease with no payer envelope is refused", func(t *testing.T) {
		bad := lease.DeepCopy()
		bad.ObjectMeta = metav1.ObjectMeta{Name: "charges-nobody", Namespace: "default"}
		bad.Spec.PaidByEnvelope = ""
		if err := c.Create(suiteCtx, bad); err == nil {
			_ = c.Delete(suiteCtx, bad)
			t.Fatal("apiserver ACCEPTED a lease that charges nobody while holding a GPU")
		} else if !apierrors.IsInvalid(err) {
			t.Fatalf("rejected with %v, want Invalid", err)
		}
	})

	t.Run("a lease with an invented role is refused", func(t *testing.T) {
		bad := lease.DeepCopy()
		bad.ObjectMeta = metav1.ObjectMeta{Name: "third-role", Namespace: "default"}
		bad.Spec.Slice.Role = "Opportunistic"
		if err := c.Create(suiteCtx, bad); err == nil {
			_ = c.Delete(suiteCtx, bad)
			t.Fatal("apiserver ACCEPTED a lease role outside {Active, Spare}; the engine folds on exactly those two")
		} else if !apierrors.IsInvalid(err) {
			t.Fatalf("rejected with %v, want Invalid", err)
		}
	})
}

package kube

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/internal/manifestcorpus"
)

// R18 done-when: the api/v1 accept/reject corpus passes through a real API
// server with the webhooks on. Dry-run creates run the whole admission chain
// (schema, mutating webhook, validating webhook) without persisting, so the
// reconcilers never react to corpus objects.

// schemaRejectedCases names the corpus cases the generated OpenAPI schema or its
// CEL rules reject before the webhook can phrase the rule. Listing them explicitly
// keeps the exception from silently absorbing new cases: a webhook regression on
// any case not named here still fails the test.
//
// The list grew a lot with R14, and that growth IS R14 — every entry is a rule that
// used to hold only while the validating webhook was up and now holds in the
// apiserver. Nothing is lost by the move: the webhook's own phrasing for every one
// of these cases stays pinned, in-process and without a cluster, by
// api/v1/validation_corpus_test.go over this same corpus. What this file still
// proves is the part only a real apiserver can: that the object is refused, by
// something, on the way in.
var schemaRejectedCases = map[string]bool{
	"envelope missing selector":     true,
	"envelope invalid sharing mode": true,

	// R14 structural markers (MinLength / Minimum / MinItems).
	"missing owner":                             true,
	"missing gpuType":                           true,
	"zero GPUs":                                 true,
	"non-positive groupGPUs":                    true,
	"negative sparesPerGroup":                   true,
	"no envelopes":                              true,
	"envelope missing name":                     true,
	"aggregate cap with no envelope references": true,
	"aggregate cap missing flavor":              true,
	"aggregate cap missing name":                true,

	// R14 CEL: min/max and the step grid. `malleable non-positive step` lands here
	// too — Minimum=1 catches the zero step, and the modulus rules report a
	// divide-by-zero alongside it, which is the schema speaking either way.
	"malleable non-positive step": true,
}

func TestWebhookRunCorpus(t *testing.T) {
	requireEnv(t)
	for i, tc := range manifestcorpus.Runs {
		t.Run(tc.Name, func(t *testing.T) {
			var run v1.Run
			if err := json.Unmarshal([]byte(tc.Manifest), &run); err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			// The corpus validates specs; names are not part of it, and
			// several manifests omit metadata entirely.
			run.Name = fmt.Sprintf("corpus-run-%02d", i)
			run.Namespace = "default"
			err := kubeClient.Create(suiteCtx, &run, client.DryRunAll)
			checkAdmission(t, err, tc.WantErr, schemaRejectedCases[tc.Name])
		})
	}
}

func TestWebhookBudgetCorpus(t *testing.T) {
	requireEnv(t)
	for i, tc := range manifestcorpus.Budgets {
		t.Run(tc.Name, func(t *testing.T) {
			var budget v1.Budget
			if err := json.Unmarshal([]byte(tc.Manifest), &budget); err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			budget.Name = fmt.Sprintf("corpus-budget-%02d", i)
			budget.Namespace = "default"
			err := kubeClient.Create(suiteCtx, &budget, client.DryRunAll)
			checkAdmission(t, err, tc.WantErr, schemaRejectedCases[tc.Name])
		})
	}
}

func checkAdmission(t *testing.T, err error, wantErr string, schemaRejected bool) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("expected the API server to accept the manifest, got: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected the API server to reject the manifest with %q, but it was accepted", wantErr)
	}
	if schemaRejected {
		// The claim is precisely "the apiserver refused this before the webhook
		// got to phrase it", so assert exactly that: an Invalid error that is NOT
		// the webhook's wording. Matching the schema's own phrasings instead
		// ("Required value", "should be at least 1 chars long", "modulus by
		// zero", …) would mean chasing apiserver message text forever, and would
		// quietly pass if the webhook started answering again.
		//
		// This ratchets both ways. Drop a marker and the webhook catches the case
		// again: its wording appears, this fails, and the entry above has to come
		// out. Drop a marker with no webhook rule behind it and the object is
		// accepted, which failed two branches up.
		if !apierrors.IsInvalid(err) {
			t.Fatalf("expected a schema-level rejection, got: %v", err)
		}
		if strings.Contains(err.Error(), wantErr) {
			t.Fatalf("case is listed in schemaRejectedCases but the WEBHOOK answered (%q); the schema rule was lost — remove the entry or restore the marker: %v", wantErr, err)
		}
		return
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("expected rejection containing %q, got: %v", wantErr, err)
	}
}

// R14 moved every field-level Lease rule into the CRD schema, so the old proof of
// the lease webhook's registration — "an empty owner would be accepted without it"
// — no longer proves anything: the apiserver refuses that on its own now. State the
// consequence rather than hide it, and prove registration a way that survives:
//
// the webhook configuration is failurePolicy=Fail, so a lease that is ACCEPTED is
// only accepted because the webhook server was reached and said yes. Unregister the
// webhook and this still passes; take the server down and it fails closed. Together
// with TestTheWebhookStillCarriesWhatOnlyItCan below (which proves the validator is
// wired to real rules), that is the honest pair.
func TestTheLeaseWebhookIsReachableAndAdmits(t *testing.T) {
	requireEnv(t)
	lease := &v1.GPULease{
		ObjectMeta: metav1.ObjectMeta{Name: "reachable-lease", Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:team",
			RunRef:         v1.RunReference{Name: "ghost", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a#0"}, Role: "Active"},
			Interval:       v1.GPULeaseInterval{Start: metav1.NewTime(baseTime)},
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
	if err := kubeClient.Create(suiteCtx, lease, client.DryRunAll); err != nil {
		t.Fatalf("a valid lease was refused; with failurePolicy=Fail this means the webhook server did not answer: %v", err)
	}
}

// The webhook is not redundant: these two rules are genuinely cross-field in a way
// the schema does not express, and they are what failurePolicy=Fail now guards.
// A regression that unregistered the validating webhook would let both through.
func TestTheWebhookStillCarriesWhatOnlyItCan(t *testing.T) {
	requireEnv(t)

	t.Run("an aggregate cap naming an undeclared envelope", func(t *testing.T) {
		budget := &v1.Budget{
			ObjectMeta: metav1.ObjectMeta{Name: "dangling-cap", Namespace: "default"},
			Spec: v1.BudgetSpec{
				Owner: "org:team",
				Envelopes: []v1.BudgetEnvelope{{
					Name: "west", Flavor: "H100-80GB",
					Selector: map[string]string{"zone": "west"}, Concurrency: 8,
				}},
				AggregateCaps: []v1.AggregateCap{{
					Name: "all", Flavor: "H100-80GB", Envelopes: []string{"east"},
				}},
			},
		}
		err := kubeClient.Create(suiteCtx, budget, client.DryRunAll)
		if err == nil || !strings.Contains(err.Error(), `references unknown envelope "east"`) {
			t.Fatalf("expected the webhook to reject a cap naming an undeclared envelope, got: %v", err)
		}
	})

	t.Run("a run that follows itself", func(t *testing.T) {
		run := &v1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "ouroboros", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:team",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
				Follow:    &v1.RunFollow{After: []string{"ouroboros"}},
			},
		}
		err := kubeClient.Create(suiteCtx, run, client.DryRunAll)
		if err == nil || !strings.Contains(err.Error(), "cannot follow itself") {
			t.Fatalf("expected the webhook to reject a self-follow, got: %v", err)
		}
	})
}

// The mutating webhook must apply Run.Default() server-side: the dry-run
// response carries the defaulted object back.
func TestWebhookAppliesRunDefaults(t *testing.T) {
	requireEnv(t)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "defaulted", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 16},
			Malleable: &v1.RunMalleability{MinTotalGPUs: 8, MaxTotalGPUs: 32, StepGPUs: 8},
		},
	}
	if err := kubeClient.Create(suiteCtx, run, client.DryRunAll); err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.Spec.Locality == nil || run.Spec.Locality.AllowCrossGroupSpread == nil || !*run.Spec.Locality.AllowCrossGroupSpread {
		t.Errorf("expected defaulting to set spec.locality.allowCrossGroupSpread=true, got %+v", run.Spec.Locality)
	}
	if run.Spec.Malleable.DesiredTotalGPUs == nil || *run.Spec.Malleable.DesiredTotalGPUs != 32 {
		t.Errorf("expected defaulting to set spec.malleable.desiredTotalGPUs=32 (maxTotalGPUs), got %v", run.Spec.Malleable.DesiredTotalGPUs)
	}
}

// Reservation specs are immutable through the API server; only status may
// change. Metadata updates must still pass (the activation test relies on
// annotating a reservation to trigger its reconciler).
func TestWebhookEnforcesReservationImmutability(t *testing.T) {
	requireEnv(t)
	resetWorld(t)
	earliest := metav1.NewTime(baseTime.Add(time.Hour))
	res := &v1.Reservation{
		ObjectMeta: metav1.ObjectMeta{Name: "frozen", Namespace: "default"},
		Spec: v1.ReservationSpec{
			RunRef:         v1.RunReference{Name: "ghost", Namespace: "default"},
			IntendedSlice:  v1.IntendedSlice{Domain: map[string]string{"region": "us-west"}},
			PayingEnvelope: "west",
			EarliestStart:  earliest,
		},
	}
	if err := kubeClient.Create(suiteCtx, res); err != nil {
		t.Fatalf("create: %v", err)
	}
	mutated := res.DeepCopy()
	mutated.Spec.EarliestStart = metav1.NewTime(baseTime.Add(2 * time.Hour))
	err := kubeClient.Update(suiteCtx, mutated)
	if err == nil || !strings.Contains(err.Error(), "spec is immutable") {
		t.Fatalf("expected spec mutation to be rejected as immutable, got: %v", err)
	}
	annotated := res.DeepCopy()
	annotated.Annotations = map[string]string{"test.rq.davidlangworthy.io/poke": "1"}
	if err := kubeClient.Update(suiteCtx, annotated); err != nil {
		t.Fatalf("metadata-only update should pass validation, got: %v", err)
	}
}

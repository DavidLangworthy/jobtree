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

// schemaRejectedCases names the corpus cases whose invalid field is
// structurally absent from the manifest (a nil map serializes as null), so
// the generated OpenAPI schema rejects them with the API server's own
// "Required value" wording before the webhook can phrase the rule. Listing
// them explicitly keeps the exception from silently absorbing new cases: a
// webhook regression on any case not named here still fails the test.
var schemaRejectedCases = map[string]bool{
	"envelope missing selector": true,
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
		if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "Required value") {
			t.Fatalf("expected a schema-level Required value rejection, got: %v", err)
		}
		return
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("expected rejection containing %q, got: %v", wantErr, err)
	}
}

// The Lease validating webhook has no corpus, so its registration is proven
// with one reject case: were the webhook silently unregistered, this
// invalid lease would be accepted (the CRD schema does not require owner to
// be non-empty).
func TestWebhookRejectsInvalidLease(t *testing.T) {
	requireEnv(t)
	lease := &v1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-lease", Namespace: "default"},
		Spec: v1.LeaseSpec{
			RunRef:         v1.RunReference{Name: "ghost", Namespace: "default"},
			Slice:          v1.LeaseSlice{Nodes: []string{"node-a#0"}, Role: "Active"},
			Interval:       v1.LeaseInterval{Start: metav1.NewTime(baseTime)},
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
	err := kubeClient.Create(suiteCtx, lease, client.DryRunAll)
	if err == nil || !strings.Contains(err.Error(), "spec.owner is required") {
		t.Fatalf("expected the lease webhook to reject the empty owner, got: %v", err)
	}
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

package kube

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// R7 pt2's whole security claim is that a tenant cannot name the principal that
// pays. The old `Run.Spec.Owner` was checked only for non-emptiness and the
// R5/R6 policy matches only `pods`, so anyone with ordinary create-Run could set
// `spec.owner: <victim>` and class Owned against the victim's envelopes. The
// field is deleted and the owner now derives from the Run's namespace, which the
// API server authenticates.
//
// "Deleted from the Go type" is not the same claim. A Run submitted as raw JSON
// still carries whatever keys the submitter wrote; what makes the channel closed
// is that the generated CRD schema is structural and PRUNES them. Nothing tested
// that, and it is the one thing only a real API server can show: the in-process
// corpus test unmarshals with encoding/json, which drops unknown fields for its
// own reasons and would keep passing even if the CRD grew the field back.
func TestRunSpecOwnerIsPrunedByTheAPIServer(t *testing.T) {
	requireEnv(t)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(v1.GroupVersion.WithKind("Run"))
	obj.SetNamespace("default")
	obj.SetName("owner-forgery-attempt")
	if err := unstructured.SetNestedMap(obj.Object, map[string]any{
		// The forgery: name someone else's principal as the payer.
		"owner": "org:ai:victim",
		"resources": map[string]any{
			"gpuType":   "H100-80GB",
			"totalGPUs": int64(8),
		},
	}, "spec"); err != nil {
		t.Fatalf("build manifest: %v", err)
	}

	if err := kubeClient.Create(suiteCtx, obj); err != nil {
		t.Fatalf("the API server must ACCEPT this Run — spec.owner is not invalid, it is unknown, "+
			"and a rejection here would mean the field still exists: %v", err)
	}
	t.Cleanup(func() { _ = kubeClient.Delete(suiteCtx, obj) })

	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(v1.GroupVersion.WithKind("Run"))
	if err := kubeClient.Get(suiteCtx, client.ObjectKeyFromObject(obj), stored); err != nil {
		t.Fatalf("read back the stored Run: %v", err)
	}
	if got, found, err := unstructured.NestedString(stored.Object, "spec", "owner"); err != nil {
		t.Fatalf("inspect stored spec.owner: %v", err)
	} else if found {
		t.Fatalf("spec.owner survived as %q: a tenant can still write the funding principal, "+
			"which is the escalation R7 pt2 exists to close", got)
	}
	// And the resources really did persist, so the absence above is pruning and
	// not a create that quietly stored nothing.
	if gpus, found, err := unstructured.NestedInt64(stored.Object, "spec", "resources", "totalGPUs"); err != nil || !found || gpus != 8 {
		t.Fatalf("expected the rest of the spec to persist (totalGPUs=8), got %d found=%v err=%v", gpus, found, err)
	}
}

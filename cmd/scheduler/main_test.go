package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/reference"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// R20's failure mode is silence, and this test is the only thing standing between the
// feature and it.
//
// The framework's EventRecorder converts the object an Event is "regarding" into an
// ObjectReference with reference.GetReference against the GLOBAL client-go scheme. When
// the kind is not registered it does not return an error to the caller — the recorder
// LOGS and drops the Event. So a plugin that mirrors every decision onto the Run would
// look completely healthy while `kubectl describe run` stayed empty forever, and every
// unit test of the emission sites would still pass, because they only prove the call was
// made.
//
// This asserts the one fact that makes those calls land: main's init registers the
// jobtree types into the scheme the recorder actually consults.
//
// Mutation check: delete the AddToScheme in init() and this fails with
// "no kind is registered for the type v1.Run".
func TestARunIsReferenceableByTheSchemeTheEventRecorderUses(t *testing.T) {
	run := &v1.Run{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "train-128", UID: "3f2a9c1e-0000-4000-8000-000000000001",
	}}

	ref, err := reference.GetReference(clientgoscheme.Scheme, run)
	if err != nil {
		t.Fatalf("a Run cannot be referenced by the recorder's scheme, so every Run-mirrored plugin Event would be silently dropped: %v", err)
	}
	if ref.Kind != "Run" {
		t.Errorf("reference kind = %q, want Run", ref.Kind)
	}
	if ref.Namespace != "default" || ref.Name != "train-128" {
		t.Errorf("reference = %s/%s, want default/train-128", ref.Namespace, ref.Name)
	}
	// The UID is what `kubectl describe run` selects on. A reference without it
	// produces Events that exist in the API and are invisible where anyone looks.
	if ref.UID != run.UID {
		t.Errorf("reference UID = %q, want %q", ref.UID, run.UID)
	}
	if ref.APIVersion != "rq.davidlangworthy.io/v1" {
		t.Errorf("reference APIVersion = %q, want rq.davidlangworthy.io/v1", ref.APIVersion)
	}
}

// The core types must survive the registration — an AddToScheme that clobbered the
// scheme would break every Event the scheduler already emits about pods.
func TestRegisteringJobtreeTypesLeavesPodsReferenceable(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "train-pod-0"}}
	if _, err := reference.GetReference(clientgoscheme.Scheme, pod); err != nil {
		t.Fatalf("a Pod is no longer referenceable: %v", err)
	}
}

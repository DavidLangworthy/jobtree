//go:build e2e

package e2e

import "testing"

// TestRunCompletesWithRealContainer is the exit-criteria test named by
// docs/project/make-it-real-plan.md Track F (TESTINFRA-4): submit a Run
// whose workload is a real container (e.g. `sh -c 'sleep 2; exit 0'`),
// assert Completed *only* via a real podSucceeded watch fed by a real
// kubelet's exit code — zero hand-injected Pod status anywhere in the
// path.
//
// It is expected, deliberate red/skip until Track B (JOBSET) lands:
// api/v1/run_types.go's RunSpec has no image/command/args/env field at all
// today (docs/project/fake-features-audit.md finding #1) — every pod
// jobtree creates hardcodes registry.k8s.io/pause (controllers/kube/
// bridge.go), so there is no way for *any* test, real cluster or not, to
// tell a pod what to run, let alone make it exit 0 or exit 1 on purpose.
// Skipping (not stubbing green, not hand-setting a phase to fake the
// missing feature) is the honest state per Track F's charter: "the *test
// cases* are gated on the trunk and are expected to fail red until it
// lands — that red is the proof the harness isn't itself fake."
//
// Do not delete this skip to make CI green; delete it only once RunSpec
// gains a real workload field and this test is rewritten to drive one.
func TestRunCompletesWithRealContainer(t *testing.T) {
	t.Skip("blocked on Track JOBSET — no real workload yet: RunSpec has no image/command field " +
		"(api/v1/run_types.go), so no Run can specify a real container for the kubelet to run to a real " +
		"exit code. See docs/project/fake-features-audit.md #1/#2 and docs/project/make-it-real-plan.md Track B.")
}

// TestRunFailsWithRealContainerExitCode is TESTINFRA-4's negative case: a
// container that exits 1 must drive the Run to Failed via a real
// failurePolicy/watch, not a hand-set corev1.PodFailed. Same blocker as
// above — Failed today can only be reached by a *_test.go hand-injection.
func TestRunFailsWithRealContainerExitCode(t *testing.T) {
	t.Skip("blocked on Track JOBSET — same as TestRunCompletesWithRealContainer: no workload field exists " +
		"to make a container exit 1 on purpose. See docs/project/make-it-real-plan.md Track B, JOBSET-8 " +
		"(failurePolicy) for the mechanism this test will drive once it lands.")
}

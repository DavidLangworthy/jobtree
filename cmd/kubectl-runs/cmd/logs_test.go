package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/davidlangworthy/jobtree/pkg/binder"
)

func activeAndSpare() []runPod {
	pods := []runPod{
		{Name: "train-g0-active-0", Role: binder.RoleActive, Group: "0"},
		{Name: "train-g0-active-1", Role: binder.RoleActive, Group: "0"},
		{Name: "train-g0-spare-0", Role: binder.RoleSpare, Group: "0"},
	}
	sortRunPods(pods)
	return pods
}

// The default answer to "show me the logs" is rank 0 — the first active member.
func TestSelectLogPodDefaultsToRankZeroActive(t *testing.T) {
	got, err := selectLogPod(activeAndSpare(), "", 0)
	if err != nil {
		t.Fatalf("selectLogPod: %v", err)
	}
	if got.Name != "train-g0-active-0" {
		t.Errorf("default rank = %s, want train-g0-active-0", got.Name)
	}
}

// --rank selects the Nth pod in the stable order.
func TestSelectLogPodRankSelects(t *testing.T) {
	got, err := selectLogPod(activeAndSpare(), "", 1)
	if err != nil {
		t.Fatalf("selectLogPod: %v", err)
	}
	if got.Name != "train-g0-active-1" {
		t.Errorf("rank 1 = %s, want train-g0-active-1", got.Name)
	}
}

// -r role narrows the candidate set, and rank then counts within it: rank 0 of
// role Spare is the first spare, not the first pod overall.
func TestSelectLogPodRoleFilterThenRank(t *testing.T) {
	got, err := selectLogPod(activeAndSpare(), binder.RoleSpare, 0)
	if err != nil {
		t.Fatalf("selectLogPod: %v", err)
	}
	if got.Name != "train-g0-spare-0" {
		t.Errorf("rank 0 of role Spare = %s, want train-g0-spare-0", got.Name)
	}
}

// An out-of-range rank is a user error with an actionable message, not a panic or
// a silently-wrong pod.
func TestSelectLogPodRankOutOfRange(t *testing.T) {
	_, err := selectLogPod(activeAndSpare(), "", 9)
	if err == nil {
		t.Fatal("expected an out-of-range error for rank 9")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error = %q, want it to say out of range", err.Error())
	}
}

// A role with no pods, or a run with no pods, is explained — not returned as an
// empty selection the caller would have to interpret.
func TestSelectLogPodEmptyAndUnknownRole(t *testing.T) {
	if _, err := selectLogPod(nil, "", 0); err == nil {
		t.Fatal("expected an error selecting from no pods")
	}
	if _, err := selectLogPod(activeAndSpare(), "Nonexistent", 0); err == nil {
		t.Fatal("expected an error selecting an absent role")
	}
}

// streamPodLog is a pass-through: whatever the opener yields is copied verbatim to
// the writer, with no parsing or buffering that could reshape a researcher's logs.
func TestStreamPodLogCopiesVerbatim(t *testing.T) {
	want := "epoch 1 loss=0.5\nepoch 2 loss=0.3\n"
	open := func(ctx context.Context, podName, container string, follow, previous bool) (io.ReadCloser, error) {
		if podName != "train-g0-active-0" {
			t.Errorf("opened pod %q, want train-g0-active-0", podName)
		}
		return io.NopCloser(strings.NewReader(want)), nil
	}
	var out bytes.Buffer
	if err := streamPodLog(context.Background(), open, &out, "train-g0-active-0", "", false, false); err != nil {
		t.Fatalf("streamPodLog: %v", err)
	}
	if out.String() != want {
		t.Errorf("streamed %q, want %q", out.String(), want)
	}
}

// The flags must reach the opener unchanged — --previous is the crashed-rank
// triage tool (R8), and dropping it silently would hand back the wrong container.
func TestStreamPodLogForwardsOptions(t *testing.T) {
	var gotContainer string
	var gotFollow, gotPrevious bool
	open := func(ctx context.Context, podName, container string, follow, previous bool) (io.ReadCloser, error) {
		gotContainer, gotFollow, gotPrevious = container, follow, previous
		return io.NopCloser(strings.NewReader("")), nil
	}
	if err := streamPodLog(context.Background(), open, io.Discard, "p", "trainer", true, true); err != nil {
		t.Fatalf("streamPodLog: %v", err)
	}
	if gotContainer != "trainer" || !gotFollow || !gotPrevious {
		t.Errorf("options forwarded as (container=%q follow=%v previous=%v), want (trainer true true)", gotContainer, gotFollow, gotPrevious)
	}
}

// An open failure is surfaced, not swallowed — a researcher must know their logs
// did not stream, and why.
func TestStreamPodLogSurfacesOpenError(t *testing.T) {
	open := func(ctx context.Context, podName, container string, follow, previous bool) (io.ReadCloser, error) {
		return nil, fmt.Errorf("pod not found")
	}
	err := streamPodLog(context.Background(), open, io.Discard, "p", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "pod not found") {
		t.Fatalf("expected the open error to surface, got %v", err)
	}
}

// logs against --local must refuse rather than fabricate: the simulator runs no
// containers, so there is nothing to stream.
func TestLogsRefusesLocal(t *testing.T) {
	opts := &RootOptions{Local: true, Namespace: "default"}
	cmd := NewLogsCommand(opts, &StateStore{}, &Printer{})
	cmd.SetArgs([]string{"train"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "live cluster") {
		t.Fatalf("expected --local to refuse with a live-cluster message, got %v", err)
	}
}

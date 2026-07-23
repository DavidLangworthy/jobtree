package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/spf13/cobra"
)

// R23: go from a Run name to a specific rank's (or a crashed rank's) logs without
// hand-built label queries. `runs logs` resolves the run's pods, picks one by
// role/rank, and streams its container log — wrapping `kubectl logs` so the
// researcher stays inside the run's mental model.
//
// Live only, and honestly so: a container log is produced by a real kubelet, and
// the --local simulator runs no containers. Fabricating output there would be the
// exact kind of fake the CLI's --local notice exists to prevent, so --local
// refuses with a pointer at the live path rather than inventing lines.
func NewLogsCommand(opts *RootOptions, _ *StateStore, _ *Printer) *cobra.Command {
	var (
		role      string
		rank      int
		container string
		follow    bool
		previous  bool
	)
	cmd := &cobra.Command{
		Use:   "logs RUN",
		Short: "Stream a Run pod's container logs, selected by role and rank",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if opts.UseLocal() {
				return fmt.Errorf("logs require a live cluster: --local runs no containers. Re-run without --local against the cluster the run is on")
			}
			c, err := opts.LiveClient()
			if err != nil {
				return err
			}
			if _, err := liveGetRun(cmd.Context(), c, opts.Namespace, name); err != nil {
				return err
			}
			pods, err := liveRunPods(cmd.Context(), c, opts.Namespace, name)
			if err != nil {
				return err
			}
			target, err := selectLogPod(pods, role, rank)
			if err != nil {
				return err
			}
			cs, err := newLiveClientset(opts.Kubeconfig, opts.KubeContext)
			if err != nil {
				return err
			}
			open := func(ctx context.Context, podName, container string, follow, previous bool) (io.ReadCloser, error) {
				return cs.CoreV1().Pods(opts.Namespace).GetLogs(podName, &corev1.PodLogOptions{
					Container: container,
					Follow:    follow,
					Previous:  previous,
				}).Stream(ctx)
			}
			return streamPodLog(cmd.Context(), open, cmd.OutOrStdout(), target.Name, container, follow, previous)
		},
	}
	cmd.Flags().StringVarP(&role, "role", "r", "", "Select pods of this role only (e.g. Active, Spare); default: any role")
	cmd.Flags().IntVar(&rank, "rank", 0, "Which pod (0-based) among the selected, ordered pods to stream; default rank 0")
	cmd.Flags().StringVarP(&container, "container", "c", "", "Container to stream; default: the pod's first container")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream new log output as it is produced")
	cmd.Flags().BoolVar(&previous, "previous", false, "Show the previous terminated container's logs (a crashed rank's last output)")
	return cmd
}

// selectLogPod resolves (role, rank) to one pod. The pods arrive already ordered
// by sortRunPods (active before spare, then group, then name), so rank N is the
// Nth pod in that stable order — rank 0 is the first active member, which is what
// a researcher means by "show me the logs" without qualification.
func selectLogPod(pods []runPod, role string, rank int) (runPod, error) {
	candidates := make([]runPod, 0, len(pods))
	for _, p := range pods {
		if role != "" && !strings.EqualFold(p.Role, role) {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		if role != "" {
			return runPod{}, fmt.Errorf("no pods of role %q for this run yet (has it started? try `runs pods`)", role)
		}
		return runPod{}, fmt.Errorf("this run has no pods yet (has it started? try `runs pods`)")
	}
	if rank < 0 || rank >= len(candidates) {
		return runPod{}, fmt.Errorf("rank %d is out of range: this run has %d matching pod(s) (ranks 0..%d)", rank, len(candidates), len(candidates)-1)
	}
	return candidates[rank], nil
}

// logOpener opens a log stream for one pod container. The command builds one over
// client-go's pods/log subresource; tests supply a stub, because the real GetLogs
// round-trip needs a live kubelet and is not unit-testable.
type logOpener func(ctx context.Context, podName, container string, follow, previous bool) (io.ReadCloser, error)

// streamPodLog copies one pod container's log to out. It is a thin wrapper: the
// CLI does not parse, buffer, or reinterpret the stream, so `runs logs` shows
// exactly what `kubectl logs` would.
func streamPodLog(ctx context.Context, open logOpener, out io.Writer, podName, container string, follow, previous bool) error {
	stream, err := open(ctx, podName, container, follow, previous)
	if err != nil {
		return fmt.Errorf("stream logs for pod %s: %w", podName, err)
	}
	defer stream.Close()
	if _, err := io.Copy(out, stream); err != nil {
		return fmt.Errorf("copy log stream for pod %s: %w", podName, err)
	}
	return nil
}

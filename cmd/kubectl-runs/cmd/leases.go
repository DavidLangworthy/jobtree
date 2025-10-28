package cmd

import (
	"fmt"
	"sort"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
	"github.com/davidlangworthy/jobtree/controllers"
)

// NewLeasesCommand lists leases associated with a Run.
func NewLeasesCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "leases RUN",
		Short: "Show active and historical leases for a Run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			state, err := store.Load(opts.StatePath)
			if err != nil {
				return err
			}
			if err := ensureRunExists(state, opts.Namespace, name); err != nil {
				return err
			}
			key := namespacedKey(opts.Namespace, name)
			leases := filterLeases(state, opts.Namespace, name)
			sort.Slice(leases, func(i, j int) bool {
				return leases[i].Spec.Interval.Start.Time.Before(leases[j].Spec.Interval.Start.Time)
			})
			rows := make([][]string, 0, len(leases))
			raw := make([]map[string]interface{}, 0, len(leases))
			for _, lease := range leases {
				quantity := len(lease.Spec.Slice.Nodes)
				if quantity == 0 {
					quantity = 1
				}
				start := lease.Spec.Interval.Start.Format(time.RFC3339)
				end := ""
				if lease.Status.Ended != nil {
					end = lease.Status.Ended.Format(time.RFC3339)
				}
				rows = append(rows, []string{
					lease.Name,
					lease.Spec.Slice.Role,
					fmt.Sprintf("%d", quantity),
					lease.Spec.PaidByEnvelope,
					start,
					end,
				})
				raw = append(raw, map[string]interface{}{
					"name":    lease.Name,
					"role":    lease.Spec.Slice.Role,
					"gpus":    quantity,
					"payer":   lease.Spec.PaidByEnvelope,
					"start":   start,
					"end":     end,
					"reason":  lease.Spec.Reason,
					"closed":  lease.Status.Closed,
					"closure": lease.Status.ClosureReason,
				})
			}
			payload := Payload{
				Headers: []string{"Lease", "Role", "GPUs", "Envelope", "Start", "End"},
				Rows:    rows,
				Raw: map[string]interface{}{
					"leases": raw,
					"run":    key,
				},
				Title: "Run Leases",
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	return cmd
}

func filterLeases(state *controllers.ClusterState, namespace, name string) []v1.Lease {
	key := namespacedKey(namespace, name)
	leases := make([]v1.Lease, 0)
	for i := range state.Leases {
		lease := state.Leases[i]
		if namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) == key {
			leases = append(leases, *lease.DeepCopy())
		}
	}
	return leases
}

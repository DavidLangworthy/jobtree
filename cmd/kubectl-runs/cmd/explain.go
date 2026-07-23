package cmd

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// NewExplainCommand produces the explain subcommand.
func NewExplainCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain RUN",
		Short: "Explain current scheduling state for a Run (read-only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if opts.UseLocal() {
				return explainLocal(cmd, opts, store, printer, name)
			}
			return explainLive(cmd, opts, printer, name)
		},
	}
	return cmd
}

func explainLocal(cmd *cobra.Command, opts *RootOptions, store *StateStore, printer *Printer, name string) error {
	state, err := store.Load(opts.StatePath)
	if err != nil {
		return err
	}
	if err := ensureRunExists(state, opts.Namespace, name); err != nil {
		return err
	}
	if err := reconcileRun(state, opts.Namespace, name); err != nil {
		return err
	}
	key := keys.NamespacedKey(opts.Namespace, name)
	run := state.Runs[key]
	payload := buildExplainPayload(state, run)
	return printer.Print(cmd, opts, payload)
}

func explainLive(cmd *cobra.Command, opts *RootOptions, printer *Printer, name string) error {
	c, err := opts.LiveClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	run, err := liveGetRun(ctx, c, opts.Namespace, name)
	if err != nil {
		return err
	}
	var reservationName string
	if run.Status.PendingReservation != nil {
		reservationName = *run.Status.PendingReservation
	}
	reservation, err := liveGetReservation(ctx, c, opts.Namespace, reservationName)
	if err != nil {
		return err
	}
	state := reservationLookupState(opts.Namespace, reservation)
	payload := buildExplainPayload(state, run)
	return printer.Print(cmd, opts, payload)
}

func buildExplainPayload(state *controllers.ClusterState, run *v1.Run) Payload {
	key := keys.NamespacedKey(run.Namespace, run.Name)
	rows := [][]string{
		{"Run", key},
		{"Phase", run.Status.Phase},
		{"Message", run.Status.Message},
	}
	// R11: the machine-readable "why". Phase says Pending; the reason on the
	// conditions says whether that is Unfunded (quota), Unschedulable (capacity),
	// GangForming (nearly there) or FollowWait (not this run's problem at all) —
	// which are four different next actions for the researcher.
	if reason := activeConditionReason(run); reason != "" {
		rows = append(rows, []string{"Reason", reason})
	}
	if run.Spec.Follow != nil && len(run.Spec.Follow.After) > 0 {
		rows = append(rows, []string{"Follows", strings.Join(run.Spec.Follow.After, ", ")})
	}
	if run.Status.Width != nil {
		rows = append(rows,
			[]string{"Width", fmt.Sprintf("desired=%d allocated=%d", run.Status.Width.Desired, run.Status.Width.Allocated)},
		)
		if run.Status.Width.Pending != "" {
			rows = append(rows, []string{"PendingWidth", run.Status.Width.Pending})
		}
	}
	if run.Status.ETA != nil {
		rows = append(rows, []string{"ETA", fmt.Sprintf("%s (%s)", run.Status.ETA.EstimatedCompletion.Time.UTC().Format(time.RFC3339), run.Status.ETA.Source)})
	}
	if run.Status.Funding != nil {
		rows = append(rows, []string{"OwnedGPUs", fmt.Sprintf("%d", run.Status.Funding.OwnedGPUs)})
		if run.Status.Funding.SharedGPUs > 0 {
			rows = append(rows, []string{"SharedGPUs", fmt.Sprintf("%d", run.Status.Funding.SharedGPUs)})
		}
		if run.Status.Funding.BorrowedGPUs > 0 {
			rows = append(rows, []string{"BorrowedGPUs", fmt.Sprintf("%d", run.Status.Funding.BorrowedGPUs)})
		}
		if run.Status.Funding.UnfundedGPUs > 0 {
			rows = append(rows, []string{"UnfundedGPUs", fmt.Sprintf("%d", run.Status.Funding.UnfundedGPUs)})
		}
		for _, lender := range run.Status.Funding.Lenders {
			rows = append(rows, []string{"Lender", fmt.Sprintf("%s (%d GPUs)", lender.Owner, lender.GPUs)})
		}
	}
	if run.Status.PendingReservation != nil {
		resKey := keys.NamespacedKey(run.Namespace, *run.Status.PendingReservation)
		if reservation := state.Reservations[resKey]; reservation != nil {
			rows = append(rows, []string{"PendingReservation", reservation.Name})
			if reservation.Status.Forecast != nil && reservation.Status.Forecast.DeficitGPUs > 0 {
				rows = append(rows, []string{"ReservationDeficit", fmt.Sprintf("%d GPUs", reservation.Status.Forecast.DeficitGPUs)})
			}
			if reservation.Status.Reason != "" {
				rows = append(rows, []string{"ReservationReason", reservation.Status.Reason})
			}
		}
	}
	return Payload{
		Headers: []string{"Field", "Value"},
		Rows:    rows,
		Raw: map[string]interface{}{
			"run": run,
		},
		Title: "Run Explanation",
	}
}

// activeConditionReason is the reason from the condition that determines the
// run's phase — the one DeriveRunPhase keyed on — or the highest-signal True
// condition when the run is merely Pending.
func activeConditionReason(run *v1.Run) string {
	for _, condType := range []string{
		v1.RunConditionFailed,
		v1.RunConditionCompleted,
		v1.RunConditionRunning,
		v1.RunConditionBlocked,
		v1.RunConditionScheduled,
		v1.RunConditionAdmitted,
	} {
		if cond := meta.FindStatusCondition(run.Status.Conditions, condType); cond != nil && cond.Status == metav1.ConditionTrue {
			return cond.Reason
		}
	}
	// Nothing is True, which is the Unschedulable/Unfunded shape: the run is not
	// admitted, and the reason for THAT is the researcher's actual question. It
	// rides on the False Admitted condition.
	if cond := meta.FindStatusCondition(run.Status.Conditions, v1.RunConditionAdmitted); cond != nil && cond.Reason != "" && cond.Reason != v1.RunReasonNotApplicable {
		return cond.Reason
	}
	return ""
}

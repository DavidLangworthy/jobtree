package cmd

import (
	"context"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// R23: the missing index from a Run to its live containers. Before this the CLI
// could show scheduling and accounting but never the workload itself, so a
// researcher had to know the pod-naming scheme and drop to raw
// `kubectl get pods -l rq.davidlangworthy.io/run=<run>` to find their own pods.
//
// NewPodsCommand lists the pods a Run owns, joined to the lease that pays for each.
func NewPodsCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pods RUN",
		Short: "List a Run's pods with their role, group, node, phase, and paying envelope",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var (
				pods   []runPod
				leases []v1.GPULease
			)
			if opts.UseLocal() {
				state, err := store.Load(opts.StatePath)
				if err != nil {
					return err
				}
				if err := ensureRunExists(state, opts.Namespace, name); err != nil {
					return err
				}
				pods = localRunPods(state, opts.Namespace, name)
				leases = filterLeases(state, opts.Namespace, name)
			} else {
				c, err := opts.LiveClient()
				if err != nil {
					return err
				}
				if _, err := liveGetRun(cmd.Context(), c, opts.Namespace, name); err != nil {
					return err
				}
				pods, err = liveRunPods(cmd.Context(), c, opts.Namespace, name)
				if err != nil {
					return err
				}
				leases, err = liveListLeases(cmd.Context(), c, opts.Namespace, name)
				if err != nil {
					return err
				}
			}
			return printer.Print(cmd, opts, buildPodsPayload(opts.Namespace, name, pods, leases))
		},
	}
	return cmd
}

// runPod is the backend-agnostic projection of one pod the pods/logs commands
// need: enough to identify it, place it in its gang, and report its state. Both
// the live corev1.Pod and the local simulator's binder.PodManifest collapse to
// this so buildPodsPayload and the rank selection in logs.go have one shape to
// reason about.
type runPod struct {
	Name     string
	Role     string
	Group    string
	Node     string
	Phase    string
	Hostname string
}

func podFromManifest(p binder.PodManifest) runPod {
	phase := p.Phase
	if phase == "" {
		phase = "Planned" // the --local simulator plans pods; it does not run them.
	}
	host := p.Hostname
	if host == "" {
		host = p.Name
	}
	return runPod{
		Name:     p.Name,
		Role:     p.Labels[binder.LabelRunRole],
		Group:    p.Labels[binder.LabelGroupIndex],
		Node:     p.NodeName,
		Phase:    phase,
		Hostname: host,
	}
}

func podFromLive(p *corev1.Pod) runPod {
	host := p.Spec.Hostname
	if host == "" {
		host = p.Name
	}
	return runPod{
		Name:     p.Name,
		Role:     p.Labels[binder.LabelRunRole],
		Group:    p.Labels[binder.LabelGroupIndex],
		Node:     p.Spec.NodeName,
		Phase:    string(p.Status.Phase),
		Hostname: host,
	}
}

// localRunPods projects the simulator's planned pods for a run.
func localRunPods(state *controllers.ClusterState, namespace, name string) []runPod {
	key := keys.NamespacedKey(namespace, name)
	out := make([]runPod, 0)
	for i := range state.Pods {
		p := state.Pods[i]
		if p.Namespace != namespace {
			continue
		}
		if keys.NamespacedKey(p.Namespace, p.Labels[binder.LabelRunName]) == key {
			out = append(out, podFromManifest(p))
		}
	}
	sortRunPods(out)
	return out
}

// liveRunPods lists a run's real pods via the run-name label index the plugin
// and controller both stamp. No client-side recompute — this is the same label
// selector `kubectl get pods -l` would use, surfaced so the researcher need not
// know it.
func liveRunPods(ctx context.Context, c client.Client, namespace, name string) ([]runPod, error) {
	var list corev1.PodList
	if err := c.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{binder.LabelRunName: name},
	); err != nil {
		return nil, err
	}
	out := make([]runPod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, podFromLive(&list.Items[i]))
	}
	sortRunPods(out)
	return out, nil
}

// sortRunPods orders pods so the listing is stable and rank-meaningful: active
// members before spares, then by group index, then by name. logs.go's --rank
// counts active pods in exactly this order.
func sortRunPods(pods []runPod) {
	sort.SliceStable(pods, func(i, j int) bool {
		a, b := pods[i], pods[j]
		if (a.Role == binder.RoleSpare) != (b.Role == binder.RoleSpare) {
			return b.Role == binder.RoleSpare // non-spare (active) first
		}
		gi, gj := groupOrd(a.Group), groupOrd(b.Group)
		if gi != gj {
			return gi < gj
		}
		return a.Name < b.Name
	})
}

// groupOrd parses a group index for ordering, sorting an absent/garbage index last.
func groupOrd(s string) int {
	if s == "" {
		return 1 << 30
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 1 << 30
	}
	return n
}

// payerByPod maps each pod name to the envelope its open lease charges, using the
// pod-name annotation the sole committer stamps at mint. This is the honest
// per-pod funding signal: the lease records the envelope that pays, and the
// class (Owned/Shared/Borrowed/Unfunded) is a run-level derivation `explain`
// reports — the CLI never re-runs pkg/funding to invent a per-pod class.
func payerByPod(leases []v1.GPULease) map[string]string {
	byPod := map[string]string{}
	for i := range leases {
		l := leases[i]
		if l.Status.Closed {
			continue
		}
		if pn := l.Annotations[binder.AnnotationPodName]; pn != "" {
			byPod[pn] = l.Spec.PaidByEnvelope
		}
	}
	return byPod
}

func buildPodsPayload(namespace, name string, pods []runPod, leases []v1.GPULease) Payload {
	payer := payerByPod(leases)
	rows := make([][]string, 0, len(pods))
	raw := make([]map[string]interface{}, 0, len(pods))
	for _, p := range pods {
		env := payer[p.Name]
		if env == "" {
			env = "-"
		}
		node := p.Node
		if node == "" {
			node = "-"
		}
		group := p.Group
		if group == "" {
			group = "-"
		}
		rows = append(rows, []string{p.Name, p.Role, group, node, p.Phase, env})
		raw = append(raw, map[string]interface{}{
			"name":     p.Name,
			"role":     p.Role,
			"group":    p.Group,
			"node":     p.Node,
			"phase":    p.Phase,
			"envelope": payer[p.Name],
			"hostname": p.Hostname,
		})
	}
	return Payload{
		Headers: []string{"Pod", "Role", "Group", "Node", "Phase", "Envelope"},
		Rows:    rows,
		Raw: map[string]interface{}{
			"pods": raw,
			"run":  keys.NamespacedKey(namespace, name),
		},
		Title: "Run Pods",
	}
}

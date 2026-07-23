package cmd

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// ArtifactsMountPath is the documented convention for where a role writes its
// outputs (checkpoints, logs-to-keep, the trained model). jobtree does not
// provide storage — a researcher mounts a volume (a PVC, an object-store CSI
// volume) at this path in their role template, and this command surfaces where
// that is. Anchoring on ONE conventional path is what lets `runs artifacts`
// answer "where do my outputs go?" without the run declaring anything new: the
// answer is read back out of the pod template the researcher already wrote.
const ArtifactsMountPath = "/artifacts"

// R23: close the loop from a Run to where its outputs land. jobtree schedules and
// funds GPUs; it does not move bytes. So rather than build a storage system, this
// reports the output volume(s) a role template mounts — by convention at
// /artifacts — so a researcher (and `explain`) can find results without reading
// raw YAML. Reads the Run spec only, so it works under --local and live alike.
func NewArtifactsCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifacts RUN",
		Short: "Show where a Run's outputs are written (the artifacts volumes its roles mount)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var run *v1.Run
			if opts.UseLocal() {
				state, err := store.Load(opts.StatePath)
				if err != nil {
					return err
				}
				if err := ensureRunExists(state, opts.Namespace, name); err != nil {
					return err
				}
				run = state.Runs[keys.NamespacedKey(opts.Namespace, name)]
			} else {
				c, err := opts.LiveClient()
				if err != nil {
					return err
				}
				run, err = liveGetRun(cmd.Context(), c, opts.Namespace, name)
				if err != nil {
					return err
				}
			}
			return printer.Print(cmd, opts, buildArtifactsPayload(opts.Namespace, run))
		},
	}
	return cmd
}

// artifactMount is one output location: a container's mount, joined to the volume
// backing it and a human description of that volume's source.
type artifactMount struct {
	Role      string
	Container string
	Path      string
	Volume    string
	Source    string
}

// collectArtifactMounts walks a run's role templates and returns every writable
// volume mount, joined to its backing volume. It reports ALL mounts, not only the
// conventional /artifacts one: a researcher who mounted their PVC at /outputs is
// asking the same question, and hiding it because the path differed would be a
// worse answer than naming it and letting them see it is non-standard.
func collectArtifactMounts(run *v1.Run) []artifactMount {
	var out []artifactMount
	for ri := range run.Spec.Roles {
		role := &run.Spec.Roles[ri]
		volumes := map[string]corev1.Volume{}
		for vi := range role.Template.Spec.Volumes {
			v := role.Template.Spec.Volumes[vi]
			volumes[v.Name] = v
		}
		roleName := role.Name
		if roleName == "" {
			roleName = fmt.Sprintf("role-%d", ri)
		}
		for ci := range role.Template.Spec.Containers {
			ctr := &role.Template.Spec.Containers[ci]
			for mi := range ctr.VolumeMounts {
				m := ctr.VolumeMounts[mi]
				if m.ReadOnly {
					continue // an input mount (dataset, config), not an output location
				}
				out = append(out, artifactMount{
					Role:      roleName,
					Container: ctr.Name,
					Path:      m.MountPath,
					Volume:    m.Name,
					Source:    describeVolumeSource(volumes[m.Name]),
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		// The conventional path first within a role, then by path.
		ip, jp := out[i].Path == ArtifactsMountPath, out[j].Path == ArtifactsMountPath
		if ip != jp {
			return ip
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// describeVolumeSource names the backing store of a volume in one human phrase.
// A PVC or CSI volume outlives the pod (results survive a crash/restart); an
// emptyDir does NOT, and saying so is the whole point — a researcher who wrote
// their checkpoints to an emptyDir will lose them on the first node failure, and
// this is where that becomes visible before it costs a training run.
func describeVolumeSource(v corev1.Volume) string {
	switch {
	case v.Name == "":
		return "(no matching volume declared — the mount names a volume the template does not define)"
	case v.PersistentVolumeClaim != nil:
		return fmt.Sprintf("PVC %s (persists across pod restarts)", v.PersistentVolumeClaim.ClaimName)
	case v.CSI != nil:
		return fmt.Sprintf("CSI %s (persists across pod restarts)", v.CSI.Driver)
	case v.NFS != nil:
		return fmt.Sprintf("NFS %s:%s (persists)", v.NFS.Server, v.NFS.Path)
	case v.HostPath != nil:
		return fmt.Sprintf("hostPath %s (node-local; lost if the pod moves nodes)", v.HostPath.Path)
	case v.EmptyDir != nil:
		return "emptyDir (EPHEMERAL — lost when the pod restarts or moves; not durable output)"
	default:
		return "(volume source not recognised)"
	}
}

func buildArtifactsPayload(namespace string, run *v1.Run) Payload {
	mounts := collectArtifactMounts(run)
	rows := make([][]string, 0, len(mounts))
	raw := make([]map[string]interface{}, 0, len(mounts))
	for _, m := range mounts {
		rows = append(rows, []string{m.Role, m.Container, m.Path, m.Volume, m.Source})
		raw = append(raw, map[string]interface{}{
			"role":      m.Role,
			"container": m.Container,
			"path":      m.Path,
			"volume":    m.Volume,
			"source":    m.Source,
		})
	}
	payload := Payload{
		Headers: []string{"Role", "Container", "Path", "Volume", "Source"},
		Rows:    rows,
		Raw: map[string]interface{}{
			"artifacts":  raw,
			"run":        keys.NamespacedKey(namespace, run.Name),
			"convention": ArtifactsMountPath,
		},
		Title: "Run Artifacts",
	}
	if len(mounts) == 0 {
		// An empty result is a real answer that needs explaining: the run keeps no
		// durable outputs. Say how to change that rather than printing a bare table.
		payload.Rows = [][]string{{
			"(none)", "-", "-", "-",
			fmt.Sprintf("this run mounts no writable output volume; mount a PVC at %s in the role template to keep results", ArtifactsMountPath),
		}}
	}
	return payload
}

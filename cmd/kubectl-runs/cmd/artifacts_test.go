package cmd

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

func runWithRole(role corev1.PodTemplateSpec) *v1.Run {
	return &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "train"},
		Spec: v1.RunSpec{
			Roles: []v1.RunRole{{Name: "trainer", Template: role}},
		},
	}
}

// collectArtifactMounts reports a writable mount joined to its backing volume, and
// classifies the volume's durability — an emptyDir "checkpoint" that will not
// survive a node failure is exactly what a researcher needs told BEFORE the run.
func TestCollectArtifactMountsClassifiesDurability(t *testing.T) {
	run := runWithRole(corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "trainer",
				VolumeMounts: []corev1.VolumeMount{
					{Name: "out", MountPath: ArtifactsMountPath},
					{Name: "scratch", MountPath: "/tmp/scratch"},
					{Name: "data", MountPath: "/data", ReadOnly: true}, // input, must be skipped
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "out", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "results"}}},
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "dataset"}}},
			},
		},
	})

	mounts := collectArtifactMounts(run)
	if len(mounts) != 2 {
		t.Fatalf("expected 2 writable mounts (readOnly skipped), got %d: %+v", len(mounts), mounts)
	}
	// The conventional /artifacts path sorts first.
	if mounts[0].Path != ArtifactsMountPath {
		t.Errorf("first mount = %s, want %s (the convention sorts first)", mounts[0].Path, ArtifactsMountPath)
	}
	if !strings.Contains(mounts[0].Source, "PVC results") || !strings.Contains(mounts[0].Source, "persists") {
		t.Errorf("PVC mount source = %q, want it to name the PVC and say it persists", mounts[0].Source)
	}
	if !strings.Contains(mounts[1].Source, "EPHEMERAL") {
		t.Errorf("emptyDir mount source = %q, want it flagged EPHEMERAL", mounts[1].Source)
	}
}

// A read-only mount is an input, not an output, and must never be reported as a
// place results are kept.
func TestCollectArtifactMountsSkipsReadOnly(t *testing.T) {
	run := runWithRole(corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:         "trainer",
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "dataset"}}}},
		},
	})
	if got := collectArtifactMounts(run); len(got) != 0 {
		t.Fatalf("read-only mounts must be skipped, got %+v", got)
	}
}

// A mount naming a volume the template does not declare is a real misconfiguration
// the researcher should see named, not silently blanked.
func TestDescribeVolumeSourceNamesMissingVolume(t *testing.T) {
	if got := describeVolumeSource(corev1.Volume{}); !strings.Contains(got, "no matching volume") {
		t.Errorf("missing-volume description = %q, want it to flag the missing declaration", got)
	}
}

// A run with no writable output volume yields a single explanatory row telling the
// researcher how to keep results — an empty table would read as "no data" rather
// than "you are keeping nothing."
func TestBuildArtifactsPayloadExplainsEmpty(t *testing.T) {
	run := runWithRole(corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "trainer"}}},
	})
	payload := buildArtifactsPayload("default", run)
	if len(payload.Rows) != 1 {
		t.Fatalf("expected one explanatory row, got %d", len(payload.Rows))
	}
	if !strings.Contains(strings.Join(payload.Rows[0], " "), ArtifactsMountPath) {
		t.Errorf("empty-artifacts row does not tell the researcher where to mount: %v", payload.Rows[0])
	}
}

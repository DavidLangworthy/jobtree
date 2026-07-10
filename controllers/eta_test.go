package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

func etaPod(name, eta string) binder.PodManifest {
	return binder.PodManifest{
		Namespace: keys.DefaultNamespace, Name: name, Phase: "Running",
		Labels:      map[string]string{binder.LabelRunName: "job", binder.LabelGroupIndex: "0", binder.LabelRunRole: binder.RoleActive},
		Annotations: map[string]string{binder.EtaAnnotation: eta},
	}
}

func TestMirrorETATakesLatestReportedByJob(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	early := now.Add(2 * time.Hour).UTC()
	late := now.Add(5 * time.Hour).UTC()
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: keys.DefaultNamespace}}
	state := &ClusterState{Pods: []binder.PodManifest{
		etaPod("p0", early.Format(time.RFC3339)),
		etaPod("p1", late.Format(time.RFC3339)),
	}}
	c := NewRunController(state, &qsClock{now: now})

	c.mirrorETA(run, now)
	if run.Status.ETA == nil {
		t.Fatalf("expected ETA mirrored")
	}
	if run.Status.ETA.Source != "job" {
		t.Errorf("source = %q, want job", run.Status.ETA.Source)
	}
	if !run.Status.ETA.EstimatedCompletion.Time.Equal(late) {
		t.Errorf("estimatedCompletion = %v, want the latest %v", run.Status.ETA.EstimatedCompletion.Time, late)
	}
	if !run.Status.ETA.ReportedAt.Time.Equal(now) {
		t.Errorf("reportedAt = %v, want now", run.Status.ETA.ReportedAt.Time)
	}
}

func TestMirrorETAClearsJobSourceButKeepsController(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: keys.DefaultNamespace}}
	c := NewRunController(&ClusterState{}, &qsClock{now: now}) // no pods report an ETA

	// A previously job-mirrored ETA is cleared once no pod reports one.
	run.Status.ETA = &v1.RunETA{EstimatedCompletion: v1.NewTime(now), Source: "job"}
	c.mirrorETA(run, now)
	if run.Status.ETA != nil {
		t.Errorf("stale job ETA should be cleared, got %+v", run.Status.ETA)
	}

	// A CLI-set (controller) ETA is left alone — the CLI owns it.
	run.Status.ETA = &v1.RunETA{EstimatedCompletion: v1.NewTime(now.Add(time.Hour)), Source: "controller"}
	c.mirrorETA(run, now)
	if run.Status.ETA == nil || run.Status.ETA.Source != "controller" {
		t.Errorf("controller ETA must be preserved, got %+v", run.Status.ETA)
	}
}

func TestMirrorETAIgnoresUnparseableAnnotation(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: keys.DefaultNamespace}}
	c := NewRunController(&ClusterState{Pods: []binder.PodManifest{etaPod("p0", "not-a-time")}}, &qsClock{now: now})
	c.mirrorETA(run, now)
	if run.Status.ETA != nil {
		t.Errorf("a garbage annotation must not set an ETA, got %+v", run.Status.ETA)
	}
}

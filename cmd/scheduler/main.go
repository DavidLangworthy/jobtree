// Command scheduler is jobtree's out-of-tree kube-scheduler-framework binary.
//
// It is a stock kube-scheduler with the jobtree plugin registered, following
// the same app.NewSchedulerCommand(app.WithPlugin(...)) pattern used by
// kubernetes-sigs/scheduler-plugins and Volcano. Selecting the plugin is a
// scheduling-config concern, not a build concern: the binary registers the
// "jobtree" plugin type, and a KubeSchedulerConfiguration profile
// (config/scheduler/jobtree-config.yaml) names schedulerName: jobtree and
// enables it at Filter/Score/Permit/PreBind/PostFilter.
//
// This is the PLUGIN-1 scaffold. The plugin bodies are no-ops today (see
// cmd/scheduler/plugin); this binary exists so the framework wiring, config
// profile, and Deployment are proven to build and register before PLUGIN-2+
// fills in real placement/funding logic.
package main

import (
	"os"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/cli"
	_ "k8s.io/component-base/logs/json/register"          // JSON log format registration
	_ "k8s.io/component-base/metrics/prometheus/clientgo" // client-go metrics registration
	_ "k8s.io/component-base/metrics/prometheus/version"  // version metric registration
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/cmd/scheduler/plugin"
)

func init() {
	// The framework's EventRecorder turns the object an Event is "regarding" into an
	// ObjectReference with reference.GetReference, against the GLOBAL client-go scheme.
	// A Run is not in it, so without this line every Run-mirrored Event the plugin emits
	// (R20) is dropped with "no kind is registered" — and dropped is the operative word:
	// the recorder logs and moves on, so the plugin would look like it was narrating
	// while `kubectl describe run` stayed empty.
	//
	// Registering here rather than in plugin.New keeps it a property of the BINARY. The
	// plugin builds its own scheme for its API client; that one is not the recorder's.
	utilruntime.Must(v1.AddToScheme(clientgoscheme.Scheme))
}

func main() {
	// Register the jobtree plugin factory under its name. The scheduler only
	// runs the plugin for profiles that enable it (the jobtree profile), so a
	// pod with the default schedulerName is unaffected.
	command := app.NewSchedulerCommand(
		app.WithPlugin(plugin.Name, plugin.New),
	)

	code := cli.Run(command)
	os.Exit(code)
}

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

	"k8s.io/component-base/cli"
	_ "k8s.io/component-base/logs/json/register"          // JSON log format registration
	_ "k8s.io/component-base/metrics/prometheus/clientgo" // client-go metrics registration
	_ "k8s.io/component-base/metrics/prometheus/version"  // version metric registration
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	"github.com/davidlangworthy/jobtree/cmd/scheduler/plugin"
)

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

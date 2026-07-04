// The jobtree controller manager: reconciles Runs, Budgets, Reservations,
// and node failures against a real Kubernetes API server.
package main

import (
	"flag"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/controllers/kube"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
)

var scheme = runtime.NewScheme()

func init() {
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		panic(err)
	}
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var enableWebhooks bool
	var accountingPeriod time.Duration

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address for metrics exposure")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address for health probes")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager")
	flag.BoolVar(&enableWebhooks, "enable-webhooks", true, "Serve the admission webhooks")
	flag.DurationVar(&accountingPeriod, "accounting-period", funding.DefaultPeriod,
		"Accounting horizon for quota evaluation: admission requires width×period of remaining GPU-hours")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
			// The engine's hand-rolled Prometheus exposition (admission
			// latency, resolver actions, budget usage) rides on the same
			// port as controller-runtime's own metrics.
			ExtraHandlers: map[string]http.Handler{"/jobtree": metrics.Handler()},
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "jobtree-manager.rq.davidlangworthy.io",
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	bridge := &kube.Bridge{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Clock:     controllers.RealClock{},
		Period:    accountingPeriod,
		// Real corev1.Events for admit/reserve/activate/resolver-action/
		// swap/complete transitions (audit findings #9 event streams, #23
		// attested seed never logged).
		Recorder: mgr.GetEventRecorderFor("jobtree"),
	}

	if err := (&kube.RunReconciler{Bridge: bridge}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create controller", "controller", "run")
		os.Exit(1)
	}
	if err := (&kube.ReservationReconciler{Bridge: bridge}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create controller", "controller", "reservation")
		os.Exit(1)
	}
	if err := (&kube.NodeReconciler{Bridge: bridge}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create controller", "controller", "node")
		os.Exit(1)
	}
	if err := (&kube.BudgetReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Clock:     controllers.RealClock{},
		Period:    accountingPeriod,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create controller", "controller", "budget")
		os.Exit(1)
	}
	if enableWebhooks {
		if err := kube.SetupWebhooks(mgr); err != nil {
			log.Error(err, "unable to register webhooks")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}

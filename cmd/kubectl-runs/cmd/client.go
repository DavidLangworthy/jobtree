package cmd

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// liveScheme is shared by every live-mode client so the CLI decodes exactly
// the types cmd/manager registers (see cmd/manager/main.go's init) — Runs,
// Budgets, Leases, and Reservations round-trip identically to what the
// controller manager itself reads and writes.
var liveScheme = func() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	return scheme
}()

// kubeconfigLoader builds a deferred client config from the standard
// kubeconfig discovery rules (KUBECONFIG env var, ~/.kube/config, in-cluster
// config), honoring an explicit --kubeconfig path and/or --context override.
// Building it never dials the API server; only ClientConfig()/Namespace()
// calls below do any I/O, and only against the kubeconfig file itself.
func kubeconfigLoader(kubeconfigPath, kubeContext string) clientcmd.ClientConfig {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
}

// resolveContextNamespace returns the namespace configured on the resolved
// kubeconfig context, if any (mirrors how `kubectl` picks a default
// namespace). It never contacts the API server.
func resolveContextNamespace(kubeconfigPath, kubeContext string) (string, bool) {
	ns, _, err := kubeconfigLoader(kubeconfigPath, kubeContext).Namespace()
	if err != nil || ns == "" {
		return "", false
	}
	return ns, true
}

// newLiveClient builds a real controller-runtime client talking to whatever
// cluster the resolved kubeconfig/context point at. This is the only place
// cmd/kubectl-runs constructs a rest.Config — every subcommand's live path
// goes through the client.Client this returns, doing real Get/List/Create/
// Update calls against the API server (no client-side scheduling/funding
// recompute).
func newLiveClient(kubeconfigPath, kubeContext string) (client.Client, error) {
	restConfig, err := kubeconfigLoader(kubeconfigPath, kubeContext).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig (pass --local to use the in-process simulator instead): %w", err)
	}
	c, err := client.New(restConfig, client.Options{Scheme: liveScheme})
	if err != nil {
		return nil, fmt.Errorf("build cluster client: %w", err)
	}
	return c, nil
}

// newLiveClientset builds a typed client-go Clientset from the same kubeconfig
// resolution as newLiveClient. It exists only for `runs logs`: streaming a
// container's log is a Pod SUBRESOURCE (`pods/log`), which the controller-runtime
// client.Client cannot request — GetLogs lives on the typed CoreV1 client. Every
// other command stays on client.Client; this is the one place that needs the
// clientset, so it is built on demand rather than threaded through RootOptions.
func newLiveClientset(kubeconfigPath, kubeContext string) (*kubernetes.Clientset, error) {
	restConfig, err := kubeconfigLoader(kubeconfigPath, kubeContext).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig (pass --local to use the in-process simulator instead): %w", err)
	}
	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	return cs, nil
}

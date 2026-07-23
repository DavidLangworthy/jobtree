package kube

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// The envtest suite runs the real manager wiring — reconcilers, bridge, and
// admission webhooks — against a real kube-apiserver. It needs the envtest
// binaries; `make envtest` provides them. Without KUBEBUILDER_ASSETS the
// suite skips so that a plain `go test ./...` stays self-contained.

var (
	testEnv     *envtest.Environment
	kubeClient  client.Client
	clock       *testClock
	skipReason  string
	suiteCtx    context.Context
	suiteBridge *Bridge // for direct reconciler invocations
	// restCfg is the apiserver's own config. R14's tests need a client that is
	// NOT the manager's cache — they delete the ValidatingWebhookConfiguration to
	// prove the CRD schema still rejects bad objects with the webhook down, and a
	// cached client would answer from a stale informer while they do it.
	restCfg *rest.Config
)

// baseTime is an arbitrary fixed instant; the engine only compares times it
// produced itself, so determinism matters more than the actual value.
var baseTime = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// `make envtest` sets JOBTREE_REQUIRE_ENVTEST: that target INTENDS to
		// run this suite, so a failure to resolve the assets must be an error,
		// not a skip. Skipping here reports `ok` for a package that ran nothing
		// — the silent pass that let a red CI merge (see IMPLEMENTATION-LOG).
		if os.Getenv("JOBTREE_REQUIRE_ENVTEST") != "" {
			fmt.Fprintln(os.Stderr, "envtest: JOBTREE_REQUIRE_ENVTEST is set but KUBEBUILDER_ASSETS is empty; refusing to skip")
			os.Exit(1)
		}
		skipReason = "KUBEBUILDER_ASSETS not set; run via `make envtest`"
		// Visible under `-v` and in CI logs. It cannot be made visible in a
		// plain `go test ./...`, which discards a passing package's output —
		// which is precisely why the real guard is `make verify` above, not
		// this banner.
		fmt.Fprintf(os.Stderr, "\n"+
			"########################################################################\n"+
			"# controllers/kube: INTEGRATION SUITE SKIPPED (%s)\n"+
			"# `go test ./...` does NOT cover the real API server. Run `make verify`.\n"+
			"########################################################################\n\n", skipReason)
		os.Exit(m.Run())
	}
	code := runSuite(m)
	os.Exit(code)
}

func runSuite(m *testing.M) int {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "config", "webhook")},
		},
	}
	// From here on the suite fails hard: KUBEBUILDER_ASSETS was provided,
	// so integration coverage is expected — a startup failure that merely
	// skipped would leave CI green with zero coverage. Stop() is safe on a
	// partially started environment and reaps any half-started processes.
	defer func() {
		if err := testEnv.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "envtest stop: %v\n", err)
		}
	}()
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: envtest failed to start: %v\n", err)
		return 1
	}

	webhookOpts := testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookOpts.LocalServingHost,
			Port:    webhookOpts.LocalServingPort,
			CertDir: webhookOpts.LocalServingCertDir,
		}),
	})
	if err != nil {
		panic(fmt.Sprintf("new manager: %v", err))
	}

	clock = &testClock{now: baseTime}
	bridge := &Bridge{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Clock:     clock,
		Recorder:  mgr.GetEventRecorderFor("jobtree"),
	}
	suiteBridge = bridge
	if err := (&RunReconciler{Bridge: bridge}).SetupWithManager(mgr); err != nil {
		panic(fmt.Sprintf("run reconciler: %v", err))
	}
	if err := (&ReservationReconciler{Bridge: bridge}).SetupWithManager(mgr); err != nil {
		panic(fmt.Sprintf("reservation reconciler: %v", err))
	}
	if err := (&NodeReconciler{Bridge: bridge}).SetupWithManager(mgr); err != nil {
		panic(fmt.Sprintf("node reconciler: %v", err))
	}
	if err := (&BudgetReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Clock:     clock,
		// Fast resync so headroom assertions converge promptly.
		ResyncPeriod: time.Second,
	}).SetupWithManager(mgr); err != nil {
		panic(fmt.Sprintf("budget reconciler: %v", err))
	}
	if err := SetupWebhooks(mgr); err != nil {
		panic(fmt.Sprintf("webhooks: %v", err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	suiteCtx = ctx
	kubeClient = mgr.GetClient()
	restCfg = cfg

	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()

	if err := waitForWebhookServer(webhookOpts); err != nil {
		panic(fmt.Sprintf("webhook server never became ready: %v", err))
	}

	code := m.Run()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			fmt.Fprintf(os.Stderr, "manager exited: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	case <-time.After(30 * time.Second):
		fmt.Fprintln(os.Stderr, "manager did not stop within 30s")
		if code == 0 {
			code = 1
		}
	}
	return code
}

func waitForWebhookServer(opts envtest.WebhookInstallOptions) error {
	addr := fmt.Sprintf("%s:%d", opts.LocalServingHost, opts.LocalServingPort)
	dialer := &net.Dialer{Timeout: time.Second}
	var lastErr error
	for range 50 {
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			return conn.Close()
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return lastErr
}

// requireEnv skips the test when the suite could not start.
func requireEnv(t *testing.T) {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
}

// eventually polls fn until it returns nil or the timeout elapses.
func eventually(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = fn(); lastErr == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %v", timeout, lastErr)
}

// resetWorld removes every jobtree object and node so tests start from an
// empty cluster. Runs go first: with no Run in the world the engine treats
// every remaining lease/reservation event as a no-op, so deletion cannot
// race a recreation.
func resetWorld(t *testing.T) {
	t.Helper()
	ctx := suiteCtx
	ns := client.InNamespace("default")

	if err := kubeClient.DeleteAllOf(ctx, &v1.Run{}, ns); err != nil {
		t.Fatalf("delete runs: %v", err)
	}
	eventually(t, 10*time.Second, func() error {
		var list v1.RunList
		if err := kubeClient.List(ctx, &list); err != nil {
			return err
		}
		if n := len(list.Items); n > 0 {
			return fmt.Errorf("%d runs remain", n)
		}
		return nil
	})

	// Sweep the dependent objects on every poll, not just once: a bridge
	// reconcile that loaded its snapshot before the runs vanished may write
	// a lease or reservation back after a single sweep has passed.
	eventually(t, 10*time.Second, func() error {
		for _, obj := range []client.Object{&v1.GPULease{}, &v1.Reservation{}, &v1.Budget{}} {
			if err := kubeClient.DeleteAllOf(ctx, obj, ns); err != nil {
				return fmt.Errorf("delete %T: %w", obj, err)
			}
		}
		// Pods need grace period zero: envtest has no kubelet to finish a
		// graceful termination, so anything else lingers forever.
		if err := kubeClient.DeleteAllOf(ctx, &corev1.Pod{}, ns, client.GracePeriodSeconds(0)); err != nil {
			return fmt.Errorf("delete pods: %w", err)
		}
		if err := kubeClient.DeleteAllOf(ctx, &corev1.Node{}); err != nil {
			return fmt.Errorf("delete nodes: %w", err)
		}
		var leases v1.GPULeaseList
		if err := kubeClient.List(ctx, &leases); err != nil {
			return err
		}
		var reservations v1.ReservationList
		if err := kubeClient.List(ctx, &reservations); err != nil {
			return err
		}
		var pods corev1.PodList
		if err := kubeClient.List(ctx, &pods); err != nil {
			return err
		}
		var nodes corev1.NodeList
		if err := kubeClient.List(ctx, &nodes); err != nil {
			return err
		}
		if n := len(leases.Items) + len(reservations.Items) + len(pods.Items) + len(nodes.Items); n > 0 {
			return fmt.Errorf("%d objects remain", n)
		}
		return nil
	})
	clock.Set(baseTime)
}

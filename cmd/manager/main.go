package main

import (
	"flag"
	"fmt"
	"time"
)

func main() {
	var metricsAddr string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address used for metrics exposure")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election (placeholder, no-op)")
	flag.Parse()

	fmt.Printf("jobtree controller manager stub\n")
	fmt.Printf("metrics: %s, leaderElection: %t\n", metricsAddr, enableLeaderElection)
	fmt.Printf("A full Kubernetes manager will be wired in a later milestone.\n")

	// Sleep briefly so CI runs can verify the binary starts without panicking.
	time.Sleep(100 * time.Millisecond)
}

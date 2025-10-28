package cmd

import (
    "errors"
    "fmt"
    "time"

    "github.com/davidlangworthy/jobtree/controllers"
)

func reconcileRun(state *controllers.ClusterState, namespace, name string) error {
    controller := controllers.NewRunController(state, controllers.RealClock{})
    return controller.Reconcile(namespace, name)
}

func reconcileAll(state *controllers.ClusterState) error {
    controller := controllers.NewRunController(state, controllers.RealClock{})
    for _, run := range state.Runs {
        if run == nil {
            continue
        }
        if err := controller.Reconcile(run.Namespace, run.Name); err != nil {
            return err
        }
    }
    return nil
}

func waitDuration(interval int) time.Duration {
    if interval <= 0 {
        return time.Second
    }
    return time.Duration(interval) * time.Second
}

func ensureRunExists(state *controllers.ClusterState, namespace, name string) error {
    key := namespacedKey(namespace, name)
    if _, ok := state.Runs[key]; !ok {
        return fmt.Errorf("run %s not found", key)
    }
    return nil
}

func copySlice[T any](src []T) []T {
    if len(src) == 0 {
        return nil
    }
    dst := make([]T, len(src))
    copy(dst, src)
    return dst
}

func uniqueAppend(list []string, value string) []string {
    for _, existing := range list {
        if existing == value {
            return list
        }
    }
    return append(list, value)
}

func requireArgument(args []string, index int, name string) (string, error) {
    if len(args) <= index {
        return "", fmt.Errorf("%s is required", name)
    }
    value := args[index]
    if value == "" {
        return "", fmt.Errorf("%s must not be empty", name)
    }
    return value, nil
}

func clampDesired(min, max, value int32) (int32, error) {
    if value < min {
        return 0, fmt.Errorf("desired GPUs %d cannot be less than min %d", value, min)
    }
    if value > max {
        return 0, fmt.Errorf("desired GPUs %d cannot exceed max %d", value, max)
    }
    return value, nil
}

var errNoMalleability = errors.New("run is not malleable")

func ensureMalleable(state *controllers.ClusterState, namespace, name string) error {
    key := namespacedKey(namespace, name)
    obj := state.Runs[key]
    if obj == nil || obj.Spec.Malleable == nil {
        return errNoMalleability
    }
    return nil
}

# `kubectl runs` plugin

The `kubectl runs` plugin provides an operator- and researcher-friendly wrapper around the Jobtree scheduler APIs.

**By default it talks to a live Kubernetes API server** — the same kubeconfig/context resolution as `kubectl` — and does real `Get`/`List`/`Create`/`Update` calls against `Run`, `Budget`, `Reservation`, and `Lease` objects. It never re-runs the scheduling/funding brain client-side: `submit` creates the object and lets the controller manager reconcile it, and commands that print status (`plan`, `explain`, `watch`, `budgets usage`) render whatever the manager already wrote.

## Installation

### krew

```bash
kubectl krew install --manifest=plugins/krew/runs.yaml
```

### From source

```bash
go build -o kubectl-runs ./cmd/kubectl-runs
```

## Live mode (default)

```bash
kubectl runs submit --file run-128-groups.yaml
kubectl runs watch train-128
kubectl runs budgets usage
```

* `--kubeconfig` selects a kubeconfig file (default: standard discovery — `$KUBECONFIG`, then `~/.kube/config`, then in-cluster config).
* `--context` selects a kubeconfig context.
* `--namespace` selects the namespace (default: the current kubeconfig context's namespace, falling back to `default`).
* `submit` accepts either YAML or JSON manifests.

## `--local` (in-process simulator)

Pass `--local` (or its synonym `--dry-run`) to instead drive an **in-process `cluster-state.json` simulator** — useful for docs, demos, and offline experimentation. This is not a cluster: no `kube-apiserver` is contacted, no controller manager reconciles anything, and no webhook runs. Every `--local` invocation prints a notice to stderr saying so.

Use the `--state` flag to select the snapshot file (default `cluster-state.json`). Commands that modify state (`submit`, `shrink`, `sponsors add`, `watch`, `complete`, `eta`) hold an advisory lock (`<state>.lock`) for the whole load-modify-save cycle and replace the snapshot atomically, so concurrent invocations cannot lose writes. Read commands (`plan`, `explain`, `budgets usage`, `leases`) never modify the state file.

```bash
kubectl runs --local --state my-cluster.json submit --file run-128-groups.yaml
```

`complete` and `eta` only work with `--local`: against a live cluster, a Run completes when its real workload pods succeed and its ETA comes from the workload reporting to the controller — the CLI does not fabricate either outcome.

## Commands

| Command | Description |
| ------- | ----------- |
| `submit` | Apply a Run manifest (YAML or JSON) and create/update it. |
| `plan` | Show the reservation plan and forecast for a Run. |
| `watch` | Continuously stream Run/Reservation status. |
| `explain` | Surface width, funding, and reservation context for a Run. |
| `budgets usage` | Summarise budget concurrency usage and headroom. |
| `sponsors list/add` | Inspect or modify borrowing sponsors. |
| `shrink` | Request a voluntary shrink for an elastic Run. |
| `leases` | List leases (active and historical) for a Run. |
| `complete` | Mark a Run's workload as finished (`--local` only). |
| `eta` | Set a Run's estimated completion time (`--local` only). |
| `completions` | Emit a shell completion script. |

## Output formats

Use `--output json` for machine-friendly output. The default `table` renders compact summaries suitable for terminals.

## Example workflow (`--local`)

```bash
kubectl runs --local --state cluster.json submit --file run-128-groups.json
kubectl runs --local --state cluster.json plan train-128
kubectl runs --local --state cluster.json watch train-128 --watch-count 3 --watch-interval 1
kubectl runs --local --state cluster.json budgets usage
```

See [docs/examples/worked-examples.md](../examples/worked-examples.md) for full end-to-end scenarios that match the CLI output.

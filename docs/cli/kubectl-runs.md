# `kubectl runs` plugin

The `kubectl runs` plugin provides an operator- and researcher-friendly wrapper around the Jobtree scheduler APIs.

## Installation

### krew

```bash
kubectl krew install --manifest=plugins/krew/runs.yaml
```

### From source

```bash
go build -o kubectl-runs ./cmd/kubectl-runs
```

## State model

The current milestone ships a local-state simulator. Commands operate on a JSON snapshot that mirrors the simplified `controllers.ClusterState` structure. Use the `--state` flag to select the file (default `cluster-state.json`).

Commands that modify state (`submit`, `shrink`, `sponsors add`, `watch`) hold an advisory lock (`<state>.lock`) for the whole load-modify-save cycle and replace the snapshot atomically, so concurrent invocations cannot lose writes. Read commands (`plan`, `explain`, `budgets usage`, `leases`) never modify the state file.

```bash
kubectl runs --state my-cluster.json submit --file run-128-groups.json
```

> **Note:** convert existing YAML manifests to JSON (for example with `yq` or `kubectl convert`) before submitting them to the simulator. Because the local CLI uses Go's standard flag parser, provide flags before positional arguments (for example, `kubectl runs sponsors add --max 4 RUN SPONSOR`).

## Commands

| Command | Description |
| ------- | ----------- |
| `submit` | Apply a JSON Run manifest and trigger immediate scheduling. |
| `plan` | Show the reservation plan and forecast for a Run. |
| `watch` | Continuously stream Run/Reservation status. |
| `explain` | Surface width, funding, and reservation context for a Run. |
| `budgets usage` | Summarise budget concurrency usage and headroom. |
| `sponsors list/add` | Inspect or modify borrowing sponsors. |
| `shrink` | Request a voluntary shrink for an elastic Run. |
| `leases` | List leases (active and historical) for a Run. |
| `completions` | Emit a lightweight bash completion script for the simulator. |

## Output formats

Use `--output json` for machine-friendly output. The default `table` renders compact summaries suitable for terminals.

## Example workflow

```bash
kubectl runs --state cluster.json submit --file run-128-groups.json
kubectl runs --state cluster.json plan train-128
kubectl runs --state cluster.json --watch-count 3 --watch-interval 1 watch train-128
kubectl runs --state cluster.json budgets usage
```

See [docs/examples/worked-examples.md](../examples/worked-examples.md) for full end-to-end scenarios that match the CLI output.

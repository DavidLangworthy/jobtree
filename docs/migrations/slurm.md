# Jobtree for SLURM Users

Jobtree borrows the good parts of SLURM (quotas, gang scheduling) while adding first-class budgets,
Reservations, and per-group spares. This page maps familiar SLURM concepts to Jobtree primitives so
you can get productive immediately.

## 1. Vocabulary bridge

| SLURM concept | Jobtree equivalent |
| --- | --- |
| Partition | Budget envelope (location + flavor selector) |
| Account/QoS | Budget hierarchy (family DAG + aggregate caps) |
| Job script (`sbatch`) | Run manifest (`kubectl runs submit -f run.yaml`) |
| `squeue` | `kubectl runs watch <run>` (per-run) or `kubectl runs state --all` |
| `scontrol show job` | `kubectl runs explain <run>` (includes Reservation + lottery proof) |
| `sacct` | `kubectl runs leases --owner <team>` (Leases are immutable usage records) |
| Reservations (`scontrol create reservation`) | Automatically generated when admission is not immediate |

## 2. Submitting jobs

SLURM script:

```bash
#!/bin/bash
#SBATCH --nodes=4
#SBATCH --gres=gpu:8
#SBATCH --time=12:00:00
srun python train.py
```

Jobtree manifest:

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata:
  name: train-32
spec:
  owner: org:ai:rai:sys
  resources:
    gpuType: H100-80GB
    totalGPUs: 32
  runtime:
    checkpoint: "30m"
  locality:
    groupGPUs: 16
```

Submit:

```bash
kubectl runs submit -f train-32.yaml
```

## 3. Watching state

* `squeue -u $USER` → `kubectl runs state --owner org:ai:rai:sys`
* `scontrol show job <id>` → `kubectl runs watch <run>` for live width + Reservation info
* `sacct -j <job>` → `kubectl runs leases <run>` (includes payer, nodes, start/end)

## 4. Reservations and fairness

SLURM reservations are manual; in Jobtree they’re automatic. When a Run cannot start now, you get:

```bash
kubectl runs plan mm3
# earliestStart: 14:05 on domain=B
# deficit: 60 GPUs
# remedies: shrink RA2 16, lottery conflict set [mm1, demo]
```

At activation time Jobtree performs structural cuts (drop spares, shrink INCR) and, if needed, runs a
public fair lottery with an attested seed. The proof is visible via `kubectl runs explain mm1`.

## 5. Preemption expectations

* SLURM priorities determine which job survives. Jobtree has **no priorities**—only budgets and
  lotteries.
* Preemption causes a Lease to end with a reason; you can see it and correlate to Reservation
  activations.
* Use `checkpoint` to control how long your job can run before writing state.

## 6. Common migration tips

1. Convert job scripts into Run manifests gradually; `kubectl runs submit -f` accepts JSON or YAML.
2. Use `groupGPUs` equal to your SLURM `--nodes × gpus-per-node` if you expect NVLink-only
   collectives.
3. Replace partitions with explicit selectors in Budgets; this keeps topology choices visible.
4. Encourage researchers to adopt `malleable` so the system can grow/shrink jobs safely—this is
   similar to SLURM’s `scontrol update JobId=<id> NumNodes=…` but automated.
5. Teach teams to inspect Reservations early; the ETA/deficit dashboards replace manual queue math.

Once you translate the vocabulary, Jobtree gives you a deterministic, auditable alternative to queue
management scripts without sacrificing fairness.

# Co-funded runs

Co-funded runs let a team draw GPUs from multiple budgets while keeping all consumption
accountable. The scheduler enforces lending policies declared on budgets and the
borrower guardrails provided on each run.

## Prerequisites

1. **Lending budget** – the lending team enables sharing on an envelope:
   ```yaml
   apiVersion: rq.davidlangworthy.io/v1
   kind: Budget
   metadata:
     name: example-vision
   spec:
     owner: org:example:vision
     envelopes:
       - name: west-h100
         flavor: H100-80GB
         selector:
           region: us-west
           cluster: gpu-a
         concurrency: 256
         lending:
           allow: true
           to:
             - org:example:*
           maxConcurrency: 64
           maxGPUHours: 20480
   ```
2. **Borrowing run** – the borrower opts-in to borrowing and declares limits:
   ```yaml
   apiVersion: rq.davidlangworthy.io/v1
   kind: Run
   metadata:
     name: train-128-cofunded
     namespace: example
   spec:
     owner: org:example:sys
     resources:
       gpuType: H100-80GB
       totalGPUs: 128
     locality:
       groupGPUs: 32
     funding:
       allowBorrow: true
       maxBorrowGPUs: 32
       sponsors:
         - org:example:vision
   ```

## What happens on admission

1. The cover planner allocates as many GPUs as possible from the run owner's envelopes.
2. Remaining demand is offered to sponsors in order. Each lending envelope must allow the
   borrower (via the ACL) and have concurrency/GPU-hour headroom.
3. The `maxBorrowGPUs` guardrail caps the number of borrowed GPUs the run can consume in a
   single admission or growth step. If the guardrail is reached the run receives a
   reservation with reason `borrow limit of <N> GPUs exhausted for requested width`.

Borrowed segments are labelled as such on the emitted leases. Budget accounting charges the
lending envelope, while run status now exposes a funding summary:

```yaml
status:
  phase: Running
  width:
    allocated: 128
    desired: 128
  funding:
    ownedGPUs: 96
    borrowedGPUs: 32
    sponsors:
      - owner: org:example:vision
        gpus: 32
```

## Inspecting who pays

* `kubectl get runs example/train-128-cofunded -o yaml` – view the funding block above.
* `kubectl runs budgets usage --owner org:example:vision` – see borrowed concurrency from the
  lending envelope (requires the CLI milestone).
* `kubectl runs explain train-128-cofunded` – confirms when borrowed groups are shrunk by the
  resolver or lottery.

## Troubleshooting

| Symptom | Explanation | Remedy |
| --- | --- | --- |
| Run stays pending with borrow-limit reason | The borrowing guardrail is lower than the required sponsor capacity. | Increase `maxBorrowGPUs` or shrink the run. |
| Borrowing denied | Lending envelope ACL does not include the borrower, or the lending policy does not set `allow: true`. | Update the lending policy and re-submit. |
| Borrowed GPUs reclaimed first | Elastic shrink prefers borrowed groups before owned ones. | Reduce desired width or increase owned budget headroom. |

Borrowed leases participate in structural cuts and lotteries exactly like owned leases; no
priority is granted purely because capacity was lent.

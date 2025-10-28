# Worked Examples for RQλ (Budget / Run / Reservation / Lease)

A collection of end-to-end scenarios that exercise cover → pack → bind, Reservations,
structural cuts, fair lottery, hot spares, elasticity, and co-funded runs. For milestone M3
the binder emits pod manifests and Lease objects directly from the cover/pack output, so the
examples focus on that immediate-start pathway.

> Domains represent fast-fabric islands. The default behavior keeps islands quiet; you only
> specify `groupGPUs` when you need groups to stay close.

---

## 1. Single-domain: budget change → reservation → lottery → failure

**Supply:** One fast-fabric domain (72 H100).

**Budgets:** Team A `concurrency=48` (open-ended); Team B `concurrency=24` (open-ended).

**Runs:**

- `J1` (Team A): fixed 32 GPUs (checkpoint 30m)
- `J2` (Team B): `INCR 8..24 step 8`
- `J3` (Team A): dev 2 GPUs

### Timeline

- **T0 — Admit.** Admit `J1(32)`, `J2(24)`, `J3(2)`. Idle: 14 GPUs.
- **T1 — Budget update.** Team A concurrency drops to 24, Team B rises to 48. Running Leases continue.
  The extra 8 GPUs for `J1` are now accounted as borrowed (no priorities, no kills).
- **T2 — New Run `J4 = 32`.** No immediate space → Reservation with `earliestStart = +15m`.
  Forecast shows deficit 8 at activation. Reservation status looks like:

  ```yaml
  status:
    state: Pending
    reason: "cluster short by 8 GPUs in scope"
    countdownSeconds: 900
    forecast:
      deficitGPUs: 8
      confidence: conservative
      remedies:
        - Drop spares in scope
        - Shrink elastic runs by step size
        - Run fair lottery if deficit remains
  ```
- **T3 — Activation (deficit).** No spares or INCR headroom on scope → enter lottery. End `J3(2)`
  then 6 ranks from `J1`; start `J4(32)`. Publish attested seed + conflict set.
- **T4 — Failure.** One node under `J2` fails → shrink `J2` from 24 → 16 (INCR). Training continues.
  Ledger records `End(Shrink)` and failure events.

### YAML Sketch

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Budget
metadata:
  name: team-a
spec:
  owner: org:team-a
  envelopes:
    - name: west-h100
      flavor: H100-80GB
      selector:
        region: us-west
        cluster: gpu-a
      concurrency: 48
```

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata:
  name: j2
  namespace: team-b
spec:
  owner: org:team-b
  resources:
    gpuType: H100-80GB
    totalGPUs: 24
  malleable:
    minTotalGPUs: 8
    maxTotalGPUs: 24
    stepGPUs: 8
```

---

## 2. Two domains + family sharing (RAI & Multimedia)

**Supply:** Domain A = 72, Domain B = 72.

**Budget DAG:** `org:ai → {RAI 64 → [AL 48, SYS 16], Multimedia 40 → [VIS 28, AUD 12]}`.
Unallocated root headroom: 16.

**Runs:**

- `RA1(AL)`: fixed 64 + 8 spares on domain A
- `RA2(SYS)`: `INCR 16..48 step 8`
- `MM1(VIS)`: fixed 48 on domain B
- `MM2(AUD)`: fixed 4 on domain B
- Later `MM3(VIS)`: fixed 64 on domain B (must start soon)

### Timeline

- **T0 — Admit.** `RA1` on A (64 + 8 spares). `RA2` at 16 on B (borrowed from RAI). `MM1(48)` +
  `MM2(4)` on B (Multimedia borrows 12 from parent).
- **T1 — `MM3` arrives (64 on B).** Reservation for B at `+20m`; deficit 60 at activation.
- **T2 — Resolve deficit.** Shrink `RA2` 16 → 0 (frees 16). Lottery ends `MM1(48)`. Activate `MM3(64)`.
  Ledger shows `Shrink` + `RandomPreempt(seed)` and each affected lease records the closure reason for
  audit via `status.closureReason`.
- **T3 — Failure on A.** Swap from spares; opportunistic tenants on spares are reclaimed.

---

## 3. Spares with opportunistic fill and deterministic reclaim

**Supply:** Domain A = 72.

**Runs:**

- `J1 (Alpha)`: 64 + 4 spares
- `J2 (Beta sweep)`: shards of 2 GPUs (up to 8 shards)

### Timeline

- **T0 — Admit `J1`.** 4 spare Leases (discounted debit) accompany active ranks.
- **T1 — Opportunistic fill.** `J2` places 4 shards (8 GPUs) on spares (`role=Borrowed`).
- **T2 — Failure.** Node under `J1` fails → reclaim spares:
  - end `J2` shards with `reason=ReclaimedBySpare`
  - start `J1` active Leases on those spare nodes
  - cordon failed node
  - controller reports `group 0 swapped to spare after node failure`; spare lease closes with `reason=Swap`

Training continues without resharding; ledger shows deterministic Start/End/Swap ordering.

---

## 4. Elastic run growth and voluntary shrink

**Supply:** Fast-fabric domain with 160 GPUs (5 × 32).

**Budget:** `org:rai` envelope `concurrency=256` covering the domain.

**Run:**

- `totalGPUs = 96`, `groupGPUs = 32`
- `malleable: { minTotalGPUs: 96, maxTotalGPUs: 160, stepGPUs: 32 }`
- `desiredTotalGPUs` defaults to 160.

### Timeline

- **T0 — Admit at min width.** Run binds 96 GPUs (three groups). Status reports:

  ```yaml
  status:
    phase: Running
    message: "bound 96 GPUs"
    width: { min: 96, max: 160, desired: 160, allocated: 96 }
  ```

- **T1 — Grow opportunistically.** Controller sees headroom and increases width
  by one step (32 GPUs). Binder emits leases with `reason: Grow`. Status becomes:

  ```yaml
  status:
    message: "grew to 128 GPUs"
    width: { min: 96, max: 160, desired: 160, allocated: 128, pending: "Grow to 160" }
  ```

- **T2 — Voluntary shrink.** User patches the Run to `desiredTotalGPUs: 96`. The
  highest-index group (and its spare, if any) closes with
  `closureReason: Shrink`, pods disappear, and status returns to:

  ```yaml
  status:
    message: "shrunk to 96 GPUs"
    width: { min: 96, max: 160, desired: 96, allocated: 96 }
  ```

Reservational shrink (resolver-triggered) still happens first when deficits
arise, but the controller now reconciles back toward the desired width once
capacity returns.

---

## 5. Co-funded Run (borrow to finish early)

**Envelopes:**

- `rai:west-h100`: concurrency 96 (window today)
- `mm:vision:west-h100`: lending enabled, `maxConcurrency: 64`

**Run:**

- `totalGPUs = 128`, `groupGPUs = 32`
- `INCR(min=96, max=128, step=32)`
- `funding.allowBorrow = true`, `maxBorrowGPUs = 32`

**Outcome:**

- Cover assigns 96 GPUs to `rai` and 32 to `mm:vision` within lending limits.
- Pack places 3 groups (96) on domain A and 1 group (32) on domain B.
- Admit starts all 128 Leases; ledger shows payer per Lease. `Run.status.funding` reports
  `ownedGPUs: 96` and `borrowedGPUs: 32` with a sponsor entry for `org:ai:mm:vision` so the split
  is visible to both teams. Structural cuts target the borrowed group only because of INCR
  semantics (payer identity provides no priority).

---

## 6. Future-dated Budget window with staged Reservation

**Envelope:** `rai:west-h100`, window starts tomorrow 00:00, `concurrency=5000`.

**Run:** Fixed 4096 GPUs submitted today.

**Admission:** `cover@today` cannot debit (no future borrowing) → create Reservation with intended
slice and `earliestStart = tomorrow 00:00:10`. Status reports `confidence: window-aligned` and
`countdownSeconds` to activation. Backfill allowed until then. At midnight, activation runs structural
cuts (if needed), starts Leases, and marks the Reservation Released.

---

## CLI Snippets (kubectl plugin)

```bash
kubectl runs submit -f run-rai-64-spares.yaml
kubectl runs plan mm3
kubectl runs explain mm1
kubectl patch run rai-sys/train-128 --type merge \
  -p '{"spec":{"malleable":{"desiredTotalGPUs":96}}}'
kubectl runs budgets usage --owner org:ai:rai:sys
```

*(A dedicated `kubectl runs shrink` helper will wrap the patch step in a later milestone.)*
EOF

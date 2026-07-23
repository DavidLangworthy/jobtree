# Day-2 runbook: break-glass, uninstall, upgrade

Three procedures, for the three moments an operator needs jobtree to get out of the
way. Each is one documented step, each has a script in `hack/`, and each is exercised
against a real cluster by `make e2e-runbook` — so if a name or an order in this page is
wrong, CI says so rather than you finding out during the incident.

Read [`admin-setup.md`](admin-setup.md) first for what is installed and why.

## The one fact that shapes all of this

**An open `GPULease` charges a budget and holds GPUs.** The ledger is a fold over
leases, and a lease nobody closes bills forever. Nothing crashes and nothing turns red.

Two consequences run through every procedure below:

- **The controller manager is what closes leases**, via the
  `rq.davidlangworthy.io/funding-closure` finalizer on each Run. Break-glass never
  scales the manager down, and uninstall deletes Runs *while the manager is still up*.
- **The scheduler plugin is the sole committer.** Stopping it stops all new funding
  instantly and safely; nothing half-commits.

---

## 1. Break-glass — jobtree is wedging the cluster

```bash
hack/break-glass.sh gpu-scheduling      # almost always this one, first
hack/break-glass.sh committing
hack/break-glass.sh crd-writes
hack/break-glass.sh all                 # all three
hack/break-glass.sh --undo committing crd-writes
```

Everything is found by the chart's own label (`app.kubernetes.io/instance`), so pass
`--release <name>` if you have more than one install, and `--namespace` if jobtree is
not in `jobtree-system`. `--dry-run` prints the `kubectl` commands without running
them.

### Lever 1 — restore GPU scheduling (`gpu-scheduling`)

**Use when:** GPU pods cannot start anywhere, for anyone.

The R6 `ValidatingAdmissionPolicyBinding` requires every pod requesting
`nvidia.com/gpu` to use `schedulerName: jobtree`. That is the whole point of it — it
closes the budget-bypass hole — and it means a wedged jobtree takes GPU scheduling in
the cluster down with it. Deleting the **binding** lifts the requirement immediately;
the policy object itself is left in place, so re-enabling is one step.

```bash
hack/break-glass.sh gpu-scheduling
kubectl apply -f your-gpu-pod.yaml       # now schedulable by the default scheduler
```

This is the most important lever and it is one command. It does **not** move pods that
are already pending on jobtree — see lever 2 for that.

**It is not instantaneous, and the script waits for it.** The apiserver evaluates
admission policies from a cached snapshot that refreshes on its own schedule, so for a
few seconds after the binding is deleted, GPU pods are *still denied* — by name, by a
binding that no longer exists. Measured on kind: the `delete` returns, the very next
create is refused quoting the deleted binding, and a moment later the same create
succeeds. `break-glass.sh` therefore dry-runs a probe pod in a loop and does not print
"GPU pods may now use any scheduler" until one is actually admitted. If you run the
`kubectl delete` by hand instead, **retry the denied pod** before concluding the lever
failed.

**Undo:** the binding belongs to the Helm release, so put it back with the release
rather than by hand:

```bash
helm upgrade <release> <chart> -n jobtree-system --set podPolicy.enabled=true
```

The lag cuts both ways: for a few seconds after the upgrade, GPU pods on the default
scheduler are still admitted. Confirm enforcement is back before you call the incident
closed — try one and expect the denial.

### Lever 2 — stop jobtree committing (`committing`)

**Use when:** jobtree is binding or funding things it should not, or you want the
world to hold still while you debug.

Scales the jobtree **scheduler** Deployment to 0. Nothing new is bound and nothing new
is funded. Pods that already say `schedulerName: jobtree` will **pend** — that is
expected and visible, not a failure. To move urgent work onto the default scheduler,
run lever 1 first (which makes `schedulerName` writable by anyone again) and resubmit
the work; `schedulerName` is immutable on an existing pod, so it is a delete-and-
resubmit, not a patch.

```bash
hack/break-glass.sh committing
hack/break-glass.sh --undo committing              # back to 1 replica
hack/break-glass.sh --undo --replicas 2 committing # or to a specific count
```

The **manager keeps running**, on purpose: it is what closes leases, so leaving it up
keeps the accounting honest while nothing new starts. If the manager itself is the
faulting component, scale it down by hand and understand that the ledger stops there —
open leases stay open and keep counting against their budgets.

### Lever 3 — unblock CRD writes (`crd-writes`)

**Use when:** you cannot create or edit Runs, Budgets, GPULeases or Reservations, and
the error names the webhook.

The CRD webhooks are `failurePolicy: Fail`, so if the manager's webhook endpoint is
unreachable, **every write to a jobtree object is refused**. This flips them to
`Ignore`.

```bash
hack/break-glass.sh crd-writes
hack/break-glass.sh --undo crd-writes    # back to Fail
```

What you lose while this is set: only the **cross-object** rules, which are the ones
the webhook alone can express — an aggregate cap may name an envelope that does not
exist, and a run may follow itself. **Field-level validation and lease immutability
still hold**, because R14 moved them into the CRD schema and its CEL rules, which the
apiserver enforces with no webhook involved. That is the whole reason this lever is
survivable: it degrades validation, it does not switch it off.

Restore `Fail` as soon as the endpoint is healthy.

---

## 2. Uninstall — in the order that orphans nothing

```bash
hack/uninstall.sh --release <name>                       # keeps the CRDs and the ledger
hack/uninstall.sh --release <name> --delete-crds --yes   # takes everything
hack/uninstall.sh --release <name> --dry-run
```

The order is forced by the one fact at the top of this page, and the two obligations
point in opposite directions: every open lease must be closed before the accounting
goes away, and the thing that closes them is a finalizer that needs the manager alive.

1. **Delete the mandatory-scheduler policy binding.** First, so that if anything below
   stalls, the cluster is already usable: GPU pods can fall back to the default
   scheduler.
2. **Delete every Run and wait for the finalizers to drain** — with the manager still
   running. This is what closes the leases. The script waits `--drain-timeout` seconds
   (default 300) and, if Runs are still `Terminating`, tells you to check the manager
   rather than continuing.
   > **Do not remove the finalizer by hand to "unstick" it.** That deletes the Run and
   > leaves its leases **open**: the ledger's last word is that the work is still
   > running and still charging. If you truly must, close the leases first.
3. **Check the ledger closed.** The script lists any lease still open and refuses to
   delete the CRDs while there are any (`--force` overrides).
4. **Remove the control plane** — `helm uninstall`, or `--no-helm` to delete the
   workloads, RBAC, webhooks and policy by label.
5. **CRDs last, and only if asked.** `helm uninstall` does not delete CRDs installed
   from a chart's `crds/` directory — Helm's rule, and the right default here, because
   the lease set **is** the audit trail: who ran where, when, and who paid. Nothing
   else in the cluster records it. `--delete-crds` deletes the four CRDs and every
   Budget, GPULease and Reservation with them.

---

## 3. Upgrade and CRD migration

**Today, every CRD change is additive** — new optional fields — so
`helm upgrade` (or `kubectl apply -f config/crd/bases`) is safe, needs no conversion,
and loses nothing. Objects written by the older version keep working; unset new fields
take their documented defaults.

> **Restart the manager after any `helm upgrade` while `webhook.generateCert=true`.**
> The chart mints a *fresh self-signed CA on every upgrade*. The webhook configurations
> get the new CA immediately; the running manager keeps serving the old certificate
> until its pod restarts, and in between **every write to a jobtree object fails** with
> `x509: certificate signed by unknown authority`. This is not hypothetical — it is what
> `make e2e-runbook` hit on its second run, which is why the target restarts the
> deployment.
>
> ```bash
> helm upgrade <release> <chart> -n jobtree-system ...
> kubectl rollout restart deployment -n jobtree-system -l app.kubernetes.io/instance=<release>
> kubectl rollout status  deployment -n jobtree-system -l app.kubernetes.io/instance=<release>
> ```
>
> For a long-lived cluster, avoid the window entirely: supply a cert-manager-issued
> Secret of the same name and set `webhook.generateCert=false`. If you are already stuck
> in that window, [lever 3](#lever-3--unblock-crd-writes-crd-writes) unblocks writes
> while the rollout happens.

The rule that keeps it that way, and it is a project-wide policy, not a preference:

> **Additive-only until a real conversion webhook exists.** No dual-read windows, no
> conversion webhooks, no migration Jobs. A genuinely breaking change is **scheduled**:
> stop the jobs, upgrade, restart them.

`status.conditions` (R11) and the CRD validation rules (R14) both landed under that
rule. Conditions are additive. The validation rules are *not* additive in effect — an
object that was accepted before may be rejected now — but they only reject objects the
webhook already rejected, so nothing that was legal has become illegal.

### The one breaking change so far: `Lease` → `GPULease`

R13 renamed the kind, because `rq.davidlangworthy.io/Lease` collided with
`coordination.k8s.io/Lease`. This was a **hard rename with no migration path**, taken
deliberately: R15 established there was no production install to migrate.

If you are running a build from before that change, treat it as a scheduled outage:

1. Drain the work: `hack/uninstall.sh --release <name>` (Runs drain, leases close).
2. Export anything you want to keep — `kubectl get leases.rq.davidlangworthy.io -A -o
   yaml > ledger-archive.yaml`. It cannot be imported into the new kind; keep it as a
   record.
3. `kubectl delete crd leases.rq.davidlangworthy.io`.
4. Upgrade, which installs `gpuleases.rq.davidlangworthy.io`.
5. Resubmit the work.

**Check your RBAC by hand after this one.** A rule granting `leases` in
`rq.davidlangworthy.io` still *parses* and grants nothing at all: the scheduler's
PreBind mint would fail with a 403 and no work would ever be funded, silently. The
resource is `gpuleases`. The `leases` rule under `coordination.k8s.io` is a different
resource and must stay — that is leader election.

### Kubernetes version skew

The scheduler binary embeds the kube-scheduler framework and therefore tracks a
Kubernetes minor version — currently **1.36** (`hack/e2e/versions.env` pins the tested
node image; `go.mod` pins the framework). Supported skew:

| Component | Supported against |
|---|---|
| jobtree scheduler | the same k8s minor it was built for, ±1 minor |
| jobtree manager | any k8s that serves `admissionregistration.k8s.io/v1` and CRD CEL (**1.30+**) |
| ValidatingAdmissionPolicy (R6) | **1.30+** (GA); the chart leaves it off by default |

Upgrade the cluster first, then jobtree. Running the scheduler more than one minor
behind the apiserver is unsupported: the framework's internal APIs are not stable
across that gap, and the failure mode is a scheduler that starts and then declines to
bind.

---

## Verification

`make e2e-runbook` stands up a kind cluster, installs the chart with the policy
enforced, and runs every procedure on this page against it: the denied GPU pod, the
lever that un-denies it, the scaled-down committer, a Run written while the webhook is
unreachable, the finalizer drain, the closed ledger, and the CRDs surviving an
uninstall that did not ask to delete them.

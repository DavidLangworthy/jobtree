--------------------------- MODULE BudgetConservation ---------------------------
(***************************************************************************)
(* Reconcilers race to admit runs against one envelope, deciding from a   *)
(* snapshot of the lease store that may be stale by the time the lease is *)
(* written (informer caches; no compare-and-swap covers a *sum* over      *)
(* separate lease objects).  With Serialized = TRUE (a single admission   *)
(* worker per envelope — the design the manager ships) the concurrency    *)
(* cap is preserved.  With Serialized = FALSE, two admissions that read   *)
(* the same snapshot overspend the envelope: run                          *)
(* `make spec-counterexamples` to see TLC produce the trace.  This is the *)
(* result that pins MaxConcurrentReconciles = 1 for the Run reconciler.   *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS Capacity,   \* envelope concurrency cap
          Runs,       \* run identities
          Request,    \* GPUs per run (uniform keeps the state space tiny)
          Serialized  \* TRUE = one admission in flight at a time

VARIABLES committed,  \* set of admitted runs (the lease store)
          snapshot,   \* run -> lease-store snapshot taken at read time
          stage,      \* run -> "idle" | "decided"
          lock        \* "free" or the run holding the admission slot

vars == <<committed, snapshot, stage, lock>>

UsageOf(S) == Cardinality(S) * Request

Init ==
    /\ committed = {}
    /\ snapshot = [r \in Runs |-> {}]
    /\ stage = [r \in Runs |-> "idle"]
    /\ lock = "free"

\* A reconciler reads the lease store.  The snapshot is fresh now and may
\* be stale by the time the decision commits.
Read(r) ==
    /\ stage[r] = "idle"
    /\ r \notin committed
    /\ IF Serialized THEN lock = "free" /\ lock' = r ELSE UNCHANGED lock
    /\ snapshot' = [snapshot EXCEPT ![r] = committed]
    /\ stage' = [stage EXCEPT ![r] = "decided"]
    /\ UNCHANGED committed

\* The snapshot showed headroom: write the lease.
Admit(r) ==
    /\ stage[r] = "decided"
    /\ UsageOf(snapshot[r]) + Request <= Capacity
    /\ committed' = committed \cup {r}
    /\ stage' = [stage EXCEPT ![r] = "idle"]
    /\ IF Serialized THEN lock' = "free" ELSE UNCHANGED lock
    /\ UNCHANGED snapshot

\* The snapshot showed no headroom: give up this round.
Refuse(r) ==
    /\ stage[r] = "decided"
    /\ UsageOf(snapshot[r]) + Request > Capacity
    /\ stage' = [stage EXCEPT ![r] = "idle"]
    /\ IF Serialized THEN lock' = "free" ELSE UNCHANGED lock
    /\ UNCHANGED <<committed, snapshot>>

Next == \E r \in Runs : Read(r) \/ Admit(r) \/ Refuse(r)

Spec == Init /\ [][Next]_vars

\* The envelope's concurrency cap is never overspent.
NoOverspend == UsageOf(committed) <= Capacity
=============================================================================

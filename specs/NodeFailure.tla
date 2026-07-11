--------------------------- MODULE NodeFailure ---------------------------
(***************************************************************************)
(* NodeFailure is a deliberately small design-level model of jobtree's      *)
(* node-failure / spare-swap / reclaim seam.  It exists to catch the bug    *)
(* classes that kept recurring in production on exactly this path:          *)
(*                                                                          *)
(* - swapping on a signal that does not prove machine death,               *)
(* - reclaiming by coarse node identity instead of exact slot identity,    *)
(* - leaking a spare on a declined swap,                                   *)
(* - order-dependent phase writes during one failure sweep,                *)
(* - fixing only the ledger plane while the workload keeps running.        *)
(*                                                                          *)
(* How closely this matches the implementation                              *)
(* -------------------------------------                                    *)
(* This is closer than a whitepaper and looser than a refinement proof.     *)
(* It mirrors the real control seam in three places:                        *)
(*                                                                          *)
(* - the reconciler's fenced/deleted failure trigger,                       *)
(* - HandleNodeFailure's pass-1 spare cleanup, pass-2 active handling,      *)
(*   and post-loop failed-run sweep,                                        *)
(* - the scheduler plugin's later PreBind mint of the swap lease.           *)
(*                                                                          *)
(* The main abstractions are intentional:                                   *)
(*                                                                          *)
(* - a fixed tiny universe of runs, groups, nodes, and slots;               *)
(* - one slot per lease identity rather than arbitrary slice sets;          *)
(* - a collapsed funding class (`Funded` / `Unfunded`) instead of the full  *)
(*   owned/shared/borrowed/unfunded derivation;                             *)
(* - a collapsed pod lifecycle (`Intent`, `Bound`, `Gone`) rather than the  *)
(*   full API-level lifecycle.                                              *)
(*                                                                          *)
(* What TLC proves here                                                     *)
(* -------------------                                                      *)
(* The clean config (`NodeFailure.cfg`) checks the intended current design.  *)
(* The bug configs reintroduce one defect class at a time and must fail:    *)
(*                                                                          *)
(* - `NodeFailureR21.cfg`          -> `NoDuplicateRank`                     *)
(* - `NodeFailureR22.cfg`          -> `ReclaimIsSlotExactAndUnfunded`       *)
(* - `NodeFailureR25.cfg`          -> `FailedNodeFullyHandled`              *)
(* - `NodeFailureDeclinedSwap.cfg` -> `TerminalHoldsNothing`                *)
(* - `NodeFailureLastWriter.cfg`   -> `PhaseIsJoin`                         *)
(* - `NodeFailureHalfPlane.cfg`    -> `PlaneAgreement`                      *)
(*                                                                          *)
(* TLC did reproduce the historical bug classes.  It did not, by itself,    *)
(* prove a new implementation bug in Go.  The main open modeling question   *)
(* left on purpose is the "stale class evaluation" issue: the Go code       *)
(* computes funding once before pass 2, while this model re-derives on the  *)
(* current state.  That deserves a separate exploratory knob if we want to  *)
(* answer it mechanically.                                                  *)
(*                                                                          *)
(* Why this is useful                                                       *)
(* -----------------                                                        *)
(* This seam is where order sensitivity and cross-plane invariants matter.  *)
(* TLC earns its keep here because it explores every lease-processing order  *)
(* by construction, instead of depending on a hand-written permutation       *)
(* harness in Go tests.  It is not a substitute for envtest, e2e, or the    *)
(* antifake rails: API wiring, informer behavior, duplicate writers, and     *)
(* other implementation-only defects are outside this model's reach.         *)
(*                                                                          *)
(* Customer promise covered                                                 *)
(* ------------------------                                                 *)
(* This model covers a narrow but safety-critical part of the product        *)
(* promise: node-failure swaps do not duplicate ranks, do not steal funded   *)
(* neighbors, do not strand immortal leases, and keep the ledger/workload    *)
(* planes consistent enough for the lease trail to stay auditable.           *)
(* It does not model pack quality, reservations, ETA correctness, elastic    *)
(* growth/shrink, follow/completion, full GPU-hour arithmetic, or            *)
(* multi-cluster behavior.                                                   *)
(*                                                                          *)
(* Kubernetes semantics imported                                            *)
(* -----------------------------                                            *)
(* This is not "a Kubernetes spec".  It imports only the thin slice of      *)
(* Kubernetes semantics that this design relies on:                          *)
(*                                                                          *)
(* - cordoned and NotReady are signals, not proof of machine death;          *)
(* - deletion / out-of-service fencing is the only safe swap trigger;        *)
(* - pods are replaced by new pods and new leases, never moved in place;     *)
(* - bind-time minting matters because there is a real swap window between   *)
(*   pod emission and lease creation.                                        *)
(*                                                                          *)
(* Queueing, scoring, DRA/device-plugin details, informer behavior, and      *)
(* most pod lifecycle detail are intentionally out of scope.                 *)
(*                                                                          *)
(* Point-in-time implementation anchors                                      *)
(* -----------------------------------                                      *)
(* - node failure trigger and fencing semantics:                             *)
(*   `controllers/kube/reconcilers.go:345-455`                              *)
(* - HandleNodeFailure, runPhaseTracker, failGroupWithoutSpare, and          *)
(*   closeRunLeases: `controllers/run_controller.go:1186-1452`              *)
(* - swap lease mint at scheduler PreBind:                                  *)
(*   `cmd/scheduler/plugin/plugin.go:244-315`                               *)
(*                                                                          *)
(* These line numbers keep the abstraction map concrete.  They are not part *)
(* of the model's semantics and will drift as the Go code moves.            *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, Sequences

CONSTANTS
  Capacity,
  RequireFence,
  SlotGranularReclaim,
  SparePassFirst,
  ReleaseSpareOnDecline,
  TrackedPhases,
  EvictBothPlanes

ASSUME
  /\ Capacity \in Nat
  /\ RequireFence \in BOOLEAN
  /\ SlotGranularReclaim \in BOOLEAN
  /\ SparePassFirst \in BOOLEAN
  /\ ReleaseSpareOnDecline \in BOOLEAN
  /\ TrackedPhases \in BOOLEAN
  /\ EvictBothPlanes \in BOOLEAN

NoNode == "NoNode"
NoSlot == <<NoNode, 99>>
UnknownClass == "UnknownClass"
UnknownPhase == "UnknownPhase"

Runs == {"A", "B", "C"}
Groups == {0, 1}
Nodes == {"n1", "n2", "n3"}
Ordinals == {0, 1}

RunPhases == {"Pending", "Running", "Failed"}
NodeStates == {"Ready", "Cordoned", "NotReady", "Fenced", "Deleted"}
PodStates == {"Intent", "Bound", "Gone"}
CloseReasons == {"None", "NodeFailure", "Swap", "SwapDeclined", "ReclaimedBySpare", "RunFailed"}
Kinds == {"Primary", "Spare", "Swap"}
CloseClasses == {UnknownClass, "Funded", "Unfunded"}
ExpectedPhases == RunPhases \cup {UnknownPhase}

Slots == {<<n, o>> : n \in Nodes, o \in Ordinals}
SlotOrNone == Slots \cup {NoSlot}
RunGroups == {<<r, g>> : r \in Runs, g \in Groups}
Ids == {<<r, g, k>> : r \in Runs, g \in Groups, k \in Kinds}

MinRunnable(r) == IF r = "B" THEN 2 ELSE 1

PrimaryPlacement(rg) ==
  IF rg = <<"A", 0>> THEN <<"n1", 0>>
  ELSE IF rg = <<"B", 0>> THEN <<"n2", 0>>
  ELSE IF rg = <<"B", 1>> THEN <<"n1", 1>>
  ELSE IF rg = <<"C", 0>> THEN <<"n2", 1>>
  ELSE NoSlot

SparePlacement(rg) ==
  IF rg = <<"A", 0>> THEN <<"n2", 0>>
  ELSE IF rg = <<"C", 0>> THEN <<"n3", 0>>
  ELSE NoSlot

InitialPhase == [r \in Runs |-> "Running"]
InitialGrace == [r \in Runs |-> 0]

LeasePriority ==
  << <<"A", 0, "Primary">>,
     <<"A", 0, "Spare">>,
     <<"C", 0, "Primary">>,
     <<"C", 0, "Spare">>,
     <<"B", 1, "Primary">>,
     <<"B", 0, "Primary">>,
     <<"A", 1, "Primary">>,
     <<"A", 1, "Spare">>,
     <<"B", 0, "Spare">>,
     <<"B", 1, "Spare">>,
     <<"C", 1, "Primary">>,
     <<"C", 1, "Spare">>,
     <<"A", 0, "Swap">>,
     <<"A", 1, "Swap">>,
     <<"B", 0, "Swap">>,
     <<"B", 1, "Swap">>,
     <<"C", 0, "Swap">>,
     <<"C", 1, "Swap">> >>

RunOf(x) == x[1]
GroupOf(x) == x[2]
KindOf(x) == x[3]
RoleOf(x) == IF KindOf(x) = "Spare" THEN "Spare" ELSE "Active"
SlotNode(s) == s[1]

Severity(p) ==
  IF p = "Running" THEN 0
  ELSE IF p = "Pending" THEN 1
  ELSE 2

JoinPhase(S) ==
  IF "Failed" \in S THEN "Failed"
  ELSE IF "Pending" \in S THEN "Pending"
  ELSE "Running"

LeaseIndex(l) == CHOOSE i \in DOMAIN LeasePriority : LeasePriority[i] = l

VARIABLES
  \* Abstracts the fenced/deleted-vs-cordoned/NotReady distinction read by
  \* nodeFailed/nodeUsable in controllers/kube/reconcilers.go:345-460.
  nodeState,
  \* Abstracts Run.Status.Phase and checkpoint grace decisions written by
  \* HandleNodeFailure and failGroupWithoutSpare in
  \* controllers/run_controller.go:1229-1432.
  runPhase,
  graceLeft,
  \* Abstracts Lease.Status.Closed, Lease.Status.Reason, and slot ownership
  \* mutated across HandleNodeFailure and closeRunLeases in
  \* controllers/run_controller.go:1193-1452.
  leaseOpen,
  leaseSlot,
  leaseReason,
  \* Abstracts the emitted swap pod and the two-plane "work really stopped"
  \* obligation around emitSwapPod / removeSparePodOnNodes and scheduler
  \* PreBind minting in controllers/run_controller.go:1312-1322 and
  \* cmd/scheduler/plugin/plugin.go:244-315.
  podState,
  podSlot,
  machinePods,
  closeClass,
  closeAgainst,
  busy,
  failedNode,
  todo,
  phaseWrites,
  expectedPhase,
  handledNodes

vars ==
  << nodeState, runPhase, graceLeft, leaseOpen, leaseSlot, leaseReason,
     podState, podSlot, machinePods, closeClass, closeAgainst,
     busy, failedNode, todo, phaseWrites, expectedPhase, handledNodes >>

Init ==
  /\ nodeState = [n \in Nodes |-> "Ready"]
  /\ runPhase = InitialPhase
  /\ graceLeft = InitialGrace
  /\ leaseOpen =
       [l \in Ids |->
         CASE KindOf(l) = "Primary" -> PrimaryPlacement(<<RunOf(l), GroupOf(l)>>) # NoSlot
           [] KindOf(l) = "Spare" -> SparePlacement(<<RunOf(l), GroupOf(l)>>) # NoSlot
           [] OTHER -> FALSE]
  /\ leaseSlot =
       [l \in Ids |->
         CASE KindOf(l) = "Primary" -> PrimaryPlacement(<<RunOf(l), GroupOf(l)>>)
           [] KindOf(l) = "Spare" -> SparePlacement(<<RunOf(l), GroupOf(l)>>)
           [] OTHER -> NoSlot]
  /\ leaseReason = [l \in Ids |-> "None"]
  /\ podState =
       [p \in Ids |->
         CASE KindOf(p) = "Primary" -> IF PrimaryPlacement(<<RunOf(p), GroupOf(p)>>) # NoSlot THEN "Bound" ELSE "Gone"
           [] KindOf(p) = "Spare" -> IF SparePlacement(<<RunOf(p), GroupOf(p)>>) # NoSlot THEN "Bound" ELSE "Gone"
           [] OTHER -> "Gone"]
  /\ podSlot =
       [p \in Ids |->
         CASE KindOf(p) = "Primary" -> PrimaryPlacement(<<RunOf(p), GroupOf(p)>>)
           [] KindOf(p) = "Spare" -> SparePlacement(<<RunOf(p), GroupOf(p)>>)
           [] OTHER -> NoSlot]
  /\ machinePods = {p \in Ids : podState[p] = "Bound"}
  /\ closeClass = [l \in Ids |-> UnknownClass]
  /\ closeAgainst = [l \in Ids |-> NoSlot]
  /\ busy = FALSE
  /\ failedNode = NoNode
  /\ todo = {}
  /\ phaseWrites = [r \in Runs |-> {}]
  /\ expectedPhase = [r \in Runs |-> UnknownPhase]
  /\ handledNodes = {}

FailureSignal(n) ==
  IF RequireFence
  THEN nodeState[n] \in {"Fenced", "Deleted"}
  ELSE nodeState[n] # "Ready"

MatchingLeases(n) ==
  {l \in Ids : leaseOpen[l] /\ leaseSlot[l] # NoSlot /\ SlotNode(leaseSlot[l]) = n}

SameNodeConflicts(slot, owner) ==
  {l \in Ids :
     /\ leaseOpen[l]
     /\ leaseSlot[l] # NoSlot
     /\ SlotNode(leaseSlot[l]) = SlotNode(slot)
     /\ RunOf(l) # owner}

ExactSlotConflicts(slot, owner) ==
  {l \in Ids :
     /\ leaseOpen[l]
     /\ leaseSlot[l] = slot
     /\ RunOf(l) # owner}

OpenActiveWidthWithout(r, l) ==
  Cardinality({k \in Ids : leaseOpen[k] /\ RunOf(k) = r /\ RoleOf(k) = "Active" /\ k # l})

IsFunded(l) ==
  /\ leaseOpen[l]
  /\ Cardinality({k \in Ids : leaseOpen[k] /\ LeaseIndex(k) < LeaseIndex(l)}) < Capacity

\* Abstracts the entry into HandleNodeFailure after the reconciler has already
\* decided that the node is failed: controllers/kube/reconcilers.go:345-371
\* and controllers/run_controller.go:1186-1191.
StartFailureSweep(n) ==
  /\ ~busy
  /\ FailureSignal(n)
  /\ MatchingLeases(n) # {}
  /\ busy' = TRUE
  /\ failedNode' = n
  /\ todo' = MatchingLeases(n)
  /\ phaseWrites' = [r \in Runs |-> {}]
  /\ expectedPhase' = [r \in Runs |-> UnknownPhase]
  /\ UNCHANGED << nodeState, runPhase, graceLeft, leaseOpen, leaseSlot, leaseReason,
                  podState, podSlot, machinePods, closeClass, closeAgainst, handledNodes >>

\* Pass 1 over spare leases on the failed node:
\* controllers/run_controller.go:1193-1222.
ProcessSpare(l) ==
  /\ busy
  /\ l \in todo
  /\ leaseOpen[l]
  /\ KindOf(l) = "Spare"
  /\ todo' = todo \ {l}
  /\ IF SparePassFirst
     THEN /\ leaseOpen' = [leaseOpen EXCEPT ![l] = FALSE]
          /\ leaseReason' = [leaseReason EXCEPT ![l] = "NodeFailure"]
          /\ podState' = [podState EXCEPT ![l] = "Gone"]
          /\ machinePods' = machinePods \ {l}
     ELSE /\ UNCHANGED << leaseOpen, leaseReason, podState, machinePods >>
  /\ UNCHANGED << nodeState, runPhase, graceLeft, leaseSlot, podSlot,
                  closeClass, closeAgainst, busy, failedNode, phaseWrites,
                  expectedPhase, handledNodes >>

\* Active lease with a usable spare and no funded exact-slot blocker.
\* This folds together the exact-slot reclaim test, the close-old / consume-
\* spare swap, and the "emit a replacement pod; mint nothing here" rule from
\* controllers/run_controller.go:1253-1327.
ProcessActiveSwap(l) ==
  LET r == RunOf(l)
      g == GroupOf(l)
      s == <<r, g, "Spare">>
      w == <<r, g, "Swap">>
      target == leaseSlot[s]
      victims ==
        IF SlotGranularReclaim
        THEN ExactSlotConflicts(target, r)
        ELSE SameNodeConflicts(target, r)
      victimRuns == {RunOf(v) : v \in victims}
      stopped ==
        (IF nodeState[failedNode] \in {"Fenced", "Deleted"} THEN {l} ELSE {})
          \cup {s}
          \cup (IF EvictBothPlanes THEN victims ELSE {})
  IN
  /\ busy
  /\ l \in todo
  /\ leaseOpen[l]
  /\ RoleOf(l) = "Active"
  /\ leaseOpen[s]
  /\ target # NoSlot
  /\ IF SlotGranularReclaim THEN \A v \in victims : ~IsFunded(v) ELSE TRUE
  /\ todo' = todo \ {l}
  /\ leaseOpen' = [x \in Ids |-> IF x = l \/ x = s \/ x \in victims THEN FALSE ELSE leaseOpen[x]]
  /\ leaseSlot' = [x \in Ids |-> IF x = w THEN target ELSE leaseSlot[x]]
  /\ leaseReason' =
       [x \in Ids |->
         IF x = l THEN "NodeFailure"
         ELSE IF x = s THEN "Swap"
         ELSE IF x \in victims THEN "ReclaimedBySpare"
         ELSE leaseReason[x]]
  /\ podState' =
       [p \in Ids |->
         IF p = w THEN "Intent"
         ELSE IF p = l \/ p = s \/ (EvictBothPlanes /\ p \in victims) THEN "Gone"
         ELSE podState[p]]
  /\ podSlot' = [p \in Ids |-> IF p = w THEN target ELSE podSlot[p]]
  /\ machinePods' = machinePods \ stopped
  /\ closeClass' =
       [x \in Ids |->
         IF x \in victims
         THEN IF IsFunded(x) THEN "Funded" ELSE "Unfunded"
         ELSE closeClass[x]]
  /\ closeAgainst' = [x \in Ids |-> IF x \in victims THEN target ELSE closeAgainst[x]]
  /\ phaseWrites' =
       [rr \in Runs |->
         phaseWrites[rr]
           \cup (IF rr = r THEN {"Running"} ELSE {})
           \cup (IF rr \in victimRuns THEN {"Pending"} ELSE {})]
  /\ IF TrackedPhases
     THEN UNCHANGED runPhase
     ELSE runPhase' =
            [rr \in Runs |->
              IF rr = r THEN "Running"
              ELSE IF rr \in victimRuns THEN "Pending"
              ELSE runPhase[rr]]
  /\ UNCHANGED << nodeState, graceLeft, busy, failedNode, expectedPhase, handledNodes >>

\* Active lease whose spare's exact target slot is held by funded work, so the
\* swap declines and the group falls back to the no-spare verdict path:
\* controllers/run_controller.go:1293-1308 and 1394-1432.
ProcessActiveDecline(l) ==
  LET r == RunOf(l)
      g == GroupOf(l)
      s == <<r, g, "Spare">>
      target == leaseSlot[s]
      exactVictims == ExactSlotConflicts(target, r)
      verdict ==
        IF OpenActiveWidthWithout(r, l) >= MinRunnable(r)
        THEN "Running"
        ELSE IF graceLeft[r] > 0 THEN "Pending" ELSE "Failed"
      released == IF ReleaseSpareOnDecline THEN {s} ELSE {}
  IN
  /\ busy
  /\ l \in todo
  /\ leaseOpen[l]
  /\ RoleOf(l) = "Active"
  /\ leaseOpen[s]
  /\ target # NoSlot
  /\ SlotGranularReclaim
  /\ \E v \in exactVictims : IsFunded(v)
  /\ todo' = todo \ {l}
  /\ leaseOpen' = [x \in Ids |-> IF x = l \/ (ReleaseSpareOnDecline /\ x = s) THEN FALSE ELSE leaseOpen[x]]
  /\ leaseReason' =
       [x \in Ids |->
         IF x = l THEN "NodeFailure"
         ELSE IF ReleaseSpareOnDecline /\ x = s THEN "SwapDeclined"
         ELSE leaseReason[x]]
  /\ podState' =
       [p \in Ids |->
         IF p = l \/ (ReleaseSpareOnDecline /\ p = s) THEN "Gone"
         ELSE podState[p]]
  /\ machinePods' =
       (machinePods
         \ (IF nodeState[failedNode] \in {"Fenced", "Deleted"} THEN {l} ELSE {}))
         \ released
  /\ phaseWrites' = [rr \in Runs |-> phaseWrites[rr] \cup (IF rr = r THEN {verdict} ELSE {})]
  /\ IF TrackedPhases
     THEN UNCHANGED runPhase
     ELSE runPhase' = [rr \in Runs |-> IF rr = r THEN verdict ELSE runPhase[rr]]
  /\ UNCHANGED << nodeState, graceLeft, leaseSlot, podSlot, closeClass,
                  closeAgainst, busy, failedNode, expectedPhase, handledNodes >>

\* Active lease with no held spare at all, or a swap lease revisited after its
\* source active was already closed.  This abstracts the direct
\* failGroupWithoutSpare path in controllers/run_controller.go:1247-1250 and
\* 1394-1432.
ProcessActiveNoSpare(l) ==
  LET r == RunOf(l)
      verdict ==
        IF OpenActiveWidthWithout(r, l) >= MinRunnable(r)
        THEN "Running"
        ELSE IF graceLeft[r] > 0 THEN "Pending" ELSE "Failed"
  IN
  /\ busy
  /\ l \in todo
  /\ leaseOpen[l]
  /\ RoleOf(l) = "Active"
  /\ (KindOf(l) = "Swap" \/ ~leaseOpen[<<r, GroupOf(l), "Spare">>])
  /\ todo' = todo \ {l}
  /\ leaseOpen' = [leaseOpen EXCEPT ![l] = FALSE]
  /\ leaseReason' = [leaseReason EXCEPT ![l] = "NodeFailure"]
  /\ podState' = [podState EXCEPT ![l] = "Gone"]
  /\ machinePods' =
       machinePods \ (IF nodeState[failedNode] \in {"Fenced", "Deleted"} THEN {l} ELSE {})
  /\ phaseWrites' = [rr \in Runs |-> phaseWrites[rr] \cup (IF rr = r THEN {verdict} ELSE {})]
  /\ IF TrackedPhases
     THEN UNCHANGED runPhase
     ELSE runPhase' = [rr \in Runs |-> IF rr = r THEN verdict ELSE runPhase[rr]]
  /\ UNCHANGED << nodeState, graceLeft, leaseSlot, podSlot, closeClass,
                  closeAgainst, busy, failedNode, expectedPhase, handledNodes >>

DropClosedWorkItem(l) ==
  /\ busy
  /\ l \in todo
  /\ ~leaseOpen[l]
  /\ todo' = todo \ {l}
  /\ UNCHANGED << nodeState, runPhase, graceLeft, leaseOpen, leaseSlot, leaseReason,
                  podState, podSlot, machinePods, closeClass, closeAgainst,
                  busy, failedNode, phaseWrites, expectedPhase, handledNodes >>

\* Post-pass fold and failed-run sweep:
\* controllers/run_controller.go:1330-1452.
FinishFailureSweep ==
  LET FinalPhase(r) ==
        IF phaseWrites[r] = {} THEN runPhase[r] ELSE JoinPhase(phaseWrites[r])
      failedRuns == {r \in Runs : FinalPhase(r) = "Failed"}
      swept ==
        {l \in Ids :
           /\ leaseOpen[l]
           /\ RunOf(l) \in failedRuns
           /\ ReleaseSpareOnDecline \/ KindOf(l) # "Spare" \/ leaseReason[l] # "None"}
      stopped == {p \in machinePods : RunOf(p) \in failedRuns /\ graceLeft[RunOf(p)] = 0}
  IN
  /\ busy
  /\ todo = {}
  /\ busy' = FALSE
  /\ handledNodes' = handledNodes \cup {failedNode}
  /\ failedNode' = NoNode
  /\ todo' = {}
  /\ expectedPhase' =
       [r \in Runs |->
         IF phaseWrites[r] = {} THEN UnknownPhase ELSE JoinPhase(phaseWrites[r])]
  /\ IF TrackedPhases
     THEN /\ runPhase' =
               [r \in Runs |->
                 IF phaseWrites[r] = {} THEN runPhase[r] ELSE JoinPhase(phaseWrites[r])]
          /\ phaseWrites' = [r \in Runs |-> {}]
     ELSE /\ UNCHANGED runPhase
          /\ phaseWrites' = [r \in Runs |-> {}]
  /\ leaseOpen' = [l \in Ids |-> IF l \in swept THEN FALSE ELSE leaseOpen[l]]
  /\ leaseReason' =
       [l \in Ids |->
         IF l \in swept /\ leaseReason[l] = "None"
         THEN "RunFailed"
         ELSE leaseReason[l]]
  /\ podState' =
       [p \in Ids |->
         IF p \in swept /\ graceLeft[RunOf(p)] = 0
         THEN "Gone"
         ELSE podState[p]]
  /\ machinePods' = machinePods \ stopped
  /\ UNCHANGED << nodeState, graceLeft, leaseSlot, podSlot, closeClass, closeAgainst >>

\* The controller emits a swap pod but does not mint the replacement lease.
\* Mint happens later at scheduler PreBind:
\* cmd/scheduler/plugin/plugin.go:244-315.
MintSwap(l) ==
  /\ ~busy
  /\ KindOf(l) = "Swap"
  /\ podState[l] = "Intent"
  /\ podSlot[l] # NoSlot
  /\ nodeState[SlotNode(podSlot[l])] = "Ready"
  /\ leaseOpen' = [leaseOpen EXCEPT ![l] = TRUE]
  /\ leaseReason' = [leaseReason EXCEPT ![l] = "None"]
  /\ podState' = [podState EXCEPT ![l] = "Bound"]
  /\ machinePods' = machinePods \cup {l}
  /\ UNCHANGED << nodeState, runPhase, graceLeft, leaseSlot, podSlot,
                  closeClass, closeAgainst, busy, failedNode, todo,
                  phaseWrites, expectedPhase, handledNodes >>

Cordon(n) ==
  /\ ~busy
  /\ nodeState[n] = "Ready"
  /\ nodeState' = [nodeState EXCEPT ![n] = "Cordoned"]
  /\ UNCHANGED << runPhase, graceLeft, leaseOpen, leaseSlot, leaseReason,
                  podState, podSlot, machinePods, closeClass, closeAgainst,
                  busy, failedNode, todo, phaseWrites, expectedPhase, handledNodes >>

MarkNotReady(n) ==
  /\ ~busy
  /\ nodeState[n] \in {"Ready", "Cordoned"}
  /\ nodeState' = [nodeState EXCEPT ![n] = "NotReady"]
  /\ UNCHANGED << runPhase, graceLeft, leaseOpen, leaseSlot, leaseReason,
                  podState, podSlot, machinePods, closeClass, closeAgainst,
                  busy, failedNode, todo, phaseWrites, expectedPhase, handledNodes >>

FenceNode(n) ==
  /\ ~busy
  /\ nodeState[n] \in {"Ready", "Cordoned", "NotReady"}
  /\ nodeState' = [nodeState EXCEPT ![n] = "Fenced"]
  /\ machinePods' = {p \in machinePods : podSlot[p] = NoSlot \/ SlotNode(podSlot[p]) # n}
  /\ UNCHANGED << runPhase, graceLeft, leaseOpen, leaseSlot, leaseReason,
                  podState, podSlot, closeClass, closeAgainst,
                  busy, failedNode, todo, phaseWrites, expectedPhase, handledNodes >>

DeleteNode(n) ==
  /\ ~busy
  /\ nodeState[n] # "Deleted"
  /\ nodeState' = [nodeState EXCEPT ![n] = "Deleted"]
  /\ machinePods' = {p \in machinePods : podSlot[p] = NoSlot \/ SlotNode(podSlot[p]) # n}
  /\ UNCHANGED << runPhase, graceLeft, leaseOpen, leaseSlot, leaseReason,
                  podState, podSlot, closeClass, closeAgainst,
                  busy, failedNode, todo, phaseWrites, expectedPhase, handledNodes >>

Next ==
  \/ \E n \in Nodes: Cordon(n) \/ MarkNotReady(n) \/ FenceNode(n) \/ DeleteNode(n)
  \/ \E n \in Nodes: StartFailureSweep(n)
  \/ \E l \in Ids: MintSwap(l)
  \/ \E l \in Ids: DropClosedWorkItem(l) \/ ProcessSpare(l) \/ ProcessActiveSwap(l) \/ ProcessActiveDecline(l) \/ ProcessActiveNoSpare(l)
  \/ FinishFailureSweep

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ nodeState \in [Nodes -> NodeStates]
  /\ runPhase \in [Runs -> RunPhases]
  /\ graceLeft \in [Runs -> 0..1]
  /\ leaseOpen \in [Ids -> BOOLEAN]
  /\ leaseSlot \in [Ids -> SlotOrNone]
  /\ leaseReason \in [Ids -> CloseReasons]
  /\ podState \in [Ids -> PodStates]
  /\ podSlot \in [Ids -> SlotOrNone]
  /\ machinePods \subseteq Ids
  /\ closeClass \in [Ids -> CloseClasses]
  /\ closeAgainst \in [Ids -> SlotOrNone]
  /\ busy \in BOOLEAN
  /\ failedNode \in Nodes \cup {NoNode}
  /\ todo \subseteq Ids
  /\ phaseWrites \in [Runs -> SUBSET RunPhases]
  /\ expectedPhase \in [Runs -> ExpectedPhases]
  /\ handledNodes \subseteq Nodes

\* R21 / fencing bug class: never two machine-live copies of one rank.
NoDuplicateRank ==
  \A rg \in RunGroups:
    Cardinality({p \in machinePods :
      /\ RunOf(p) = rg[1]
      /\ GroupOf(p) = rg[2]
      /\ RoleOf(p) = "Active"}) <= 1

NoOverdraft ==
  Cardinality({l \in Ids : IsFunded(l)}) <= Capacity

\* R22 / coarse reclaim bug class.
ReclaimIsSlotExactAndUnfunded ==
  \A l \in Ids:
    leaseReason[l] = "ReclaimedBySpare"
      => /\ closeClass[l] = "Unfunded"
         /\ closeAgainst[l] = leaseSlot[l]

\* R25 / spare-only node leak bug class.
FailedNodeFullyHandled ==
  \A n \in handledNodes:
    \A l \in Ids:
      ~(leaseOpen[l] /\ leaseSlot[l] # NoSlot /\ SlotNode(leaseSlot[l]) = n)

\* Immortal-lease bug class on terminal runs.
TerminalHoldsNothing ==
  ~busy =>
    \A r \in Runs:
      runPhase[r] = "Failed"
        => \A l \in Ids: RunOf(l) # r \/ ~leaseOpen[l]

\* Mirrors the "worst verdict wins" tracker contract in
\* controllers/run_controller.go:1355-1383.
\* Last-writer-wins bug class.
PhaseIsJoin ==
  \A r \in Runs:
    expectedPhase[r] = UnknownPhase \/ runPhase[r] = expectedPhase[r]

\* Half-plane eviction bug class: the ledger and workload planes agree on who
\* is still running.
PlaneAgreement ==
  /\ \A l \in Ids:
       /\ leaseReason[l] \in {"ReclaimedBySpare", "RunFailed"}
       /\ graceLeft[RunOf(l)] = 0
       => l \notin machinePods
  /\ \A p \in Ids:
       /\ podState[p] = "Bound"
       /\ RoleOf(p) = "Active"
       => leaseOpen[p]

=============================================================================

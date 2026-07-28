------------------------- MODULE PhysicalCapacity -------------------------
(***************************************************************************)
(* A small composed model of the physical-GPU ownership seam shared by:     *)
(*                                                                         *)
(* - ordinary controller admission;                                        *)
(* - due Reservation activation;                                           *)
(* - kube-scheduler Filter/Assume/Permit/PreBind/Bind/Unreserve; and        *)
(* - a node-failure swap onto capacity released by a held spare.            *)
(*                                                                         *)
(* The load-bearing distinction is deliberately explicit:                  *)
(*                                                                         *)
(* - machineLive is the physical plane. A Kubernetes-bound or terminating   *)
(*   GPU-requesting pod holds capacity;                                     *)
(* - podState Assumed/Permitted/Prebound is scheduler-cache commitment, not  *)
(*   physical occupancy;                                                    *)
(* - leaseOpen/mintCount is accounting evidence created at PreBind;          *)
(* - reservationState is a future promise; and                              *)
(* - Pending/Observed/Assumed swap pods are acquisition attempts.            *)
(*                                                                         *)
(* Lease node#ordinal strings are NOT physical device identities here.      *)
(* SlotForNode intentionally gives every lease on one node the same "#0"    *)
(* string, so duplicate open lease slots are ordinary legal input states.    *)
(* The node prefix still records accounting placement and must match a bound *)
(* pod's node; only the ordinal is deliberately non-authoritative. Capacity  *)
(* safety is stated only over machineLive and podNode.                        *)
(*                                                                         *)
(* Kubernetes framework correction                                         *)
(* --------------------------------                                        *)
(* jobtree's Reserve plugin is a no-op. Kubernetes first chooses a node and *)
(* atomically AssumePod's the real nvidia.com/gpu request into the scheduler *)
(* cache. Reserve, Permit, PreBind, and Bind follow. Any binding-cycle error  *)
(* runs Unreserve and ForgetPod. Thus AtomicCapacityCommit models the        *)
(* scheduler cache's assume lock/revalidation, not jobtree Reserve and not a *)
(* Lease. PreBind is later and is the sole lease mint.                       *)
(*                                                                         *)
(* This is a bounded design model, not a refinement proof of Kubernetes or   *)
(* controller-runtime. It does not model device-plugin allocation, DRA, API  *)
(* watch delivery, gang widths above one, funding arithmetic, slot ordinals, *)
(* checkpoint deadlines, or arbitrary pod deletion grace. Recovery is a     *)
(* quiescence obligation, not a fairness theorem: the model never assumes an *)
(* abandoned PreBind will magically retry. MaxSteps is an explicit bounded   *)
(* reachability horizon, not an unbounded temporal proof.                    *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
  InitMode,
  AtomicCapacityCommit,
  ClosedLeaseFreesPhysical,
  MaterializationGuard,
  SwapChecksCapacity,
  IdempotentPreBind,
  RetryPlacementGuard,
  RequireBindFailureCleanup,
  MaxSteps

ASSUME
  /\ InitMode \in
       {"Lifecycle", "LedgerOversubscription", "CordonedNode",
        "SpareBeforeActive", "RunningAwaitingMint"}
  /\ AtomicCapacityCommit \in BOOLEAN
  /\ ClosedLeaseFreesPhysical \in BOOLEAN
  /\ MaterializationGuard \in BOOLEAN
  /\ SwapChecksCapacity \in BOOLEAN
  /\ IdempotentPreBind \in BOOLEAN
  /\ RetryPlacementGuard \in BOOLEAN
  /\ RequireBindFailureCleanup \in BOOLEAN
  /\ MaxSteps \in Nat

Nodes == {"n1", "n2"}
Capacity == 2

Direct == "direct"
Reserved == "reserved"
Peer == "peer"
Old == "old"
Spare == "spare"
Blocker == "blocker"
Swap == "swap"

Pods == {Direct, Reserved, Peer, Old, Spare, Blocker, Swap}
AdmissionPods == {Direct, Reserved}
ActivePods == {Direct, Reserved, Peer, Old, Blocker, Swap}
SwapRunPods == {Old, Spare, Swap}

AdmissionRun == "admission-run"
PeerRun == "peer-run"
SwapRun == "swap-run"
BlockerRun == "blocker-run"
Runs == {AdmissionRun, PeerRun, SwapRun, BlockerRun}

NoNode == "NoNode"
NoSlot == "NoSlot"
NodesOrNone == Nodes \cup {NoNode}
SlotsOrNone == {"n1#0", "n2#0", NoSlot}

PodStates ==
  {"Absent", "Pending", "Observed", "Assumed", "Permitted", "Prebound",
   "BindFailed", "Bound", "Terminating", "Gone"}
NodeStates == {"Ready", "Cordoned", "Fenced"}
ReservationStates == {"Due", "Released"}
RunPhases == {"Pending", "Running"}
Writers == {"None", "Scheduler", "Controller"}

RankOf(p) ==
  CASE p \in AdmissionPods -> "admission-rank"
    [] p = Peer -> "peer-rank"
    [] p \in {Old, Swap} -> "swap-rank"
    [] p = Spare -> "spare-rank"
    [] OTHER -> "blocker-rank"

RunOf(p) ==
  CASE p \in AdmissionPods -> AdmissionRun
    [] p = Peer -> PeerRun
    [] p \in SwapRunPods -> SwapRun
    [] OTHER -> BlockerRun

RoleOf(p) == IF p = Spare THEN "Spare" ELSE "Active"

SlotForNode(n) == IF n = "n1" THEN "n1#0" ELSE "n2#0"
SlotNode(s) ==
  CASE s = "n1#0" -> "n1"
    [] s = "n2#0" -> "n2"
    [] OTHER -> NoNode

InitialPodState(p) ==
  CASE InitMode = "Lifecycle" ->
         IF p \in {Old, Spare, Blocker} THEN "Bound" ELSE "Absent"
    [] InitMode = "LedgerOversubscription" ->
         IF p \in {Direct, Blocker} THEN "Bound" ELSE "Gone"
    [] InitMode = "CordonedNode" ->
         IF p = Old THEN "Bound" ELSE "Gone"
    [] InitMode = "SpareBeforeActive" ->
         IF p = Spare THEN "Bound" ELSE "Gone"
    [] OTHER ->
         IF p = Swap THEN "Pending" ELSE "Gone"

InitialPodNode(p) ==
  CASE InitMode = "Lifecycle" ->
         IF p = Old THEN "n1"
         ELSE IF p \in {Spare, Blocker} THEN "n2"
         ELSE NoNode
    [] InitMode = "LedgerOversubscription" ->
         IF p = Direct THEN "n1"
         ELSE IF p = Blocker THEN "n2"
         ELSE NoNode
    [] InitMode = "CordonedNode" ->
         IF p = Old THEN "n1" ELSE NoNode
    [] InitMode = "SpareBeforeActive" ->
         IF p = Spare THEN "n2" ELSE NoNode
    [] OTHER ->
         IF p = Swap THEN "n2" ELSE NoNode

InitialLeaseOpen(p) ==
  CASE InitMode = "Lifecycle" ->
         IF p \in {Old, Spare, Blocker} THEN 1 ELSE 0
    [] InitMode = "LedgerOversubscription" ->
         IF p \in {Direct, Blocker} THEN 1 ELSE 0
    [] InitMode = "CordonedNode" ->
         IF p = Old THEN 1 ELSE 0
    [] InitMode = "SpareBeforeActive" ->
         IF p = Spare THEN 1 ELSE 0
    [] OTHER -> 0

InitialLeaseSlot(p) ==
  CASE InitMode = "LedgerOversubscription" ->
         IF p \in {Direct, Blocker} THEN "n1#0" ELSE NoSlot
    [] InitialLeaseOpen(p) = 1 -> SlotForNode(InitialPodNode(p))
    [] OTHER -> NoSlot

InitialNodeState(n) ==
  IF InitMode = "CordonedNode" /\ n = "n1" THEN "Cordoned" ELSE "Ready"

InitialRunPhase(r) ==
  CASE InitMode = "RunningAwaitingMint" ->
         IF r = SwapRun THEN "Running" ELSE "Pending"
    [] InitMode = "Lifecycle" ->
         IF r \in {SwapRun, BlockerRun} THEN "Running" ELSE "Pending"
    [] InitMode = "LedgerOversubscription" ->
         IF r \in {AdmissionRun, BlockerRun} THEN "Running" ELSE "Pending"
    [] InitMode = "CordonedNode" ->
         IF r = SwapRun THEN "Running" ELSE "Pending"
    [] InitMode = "SpareBeforeActive" ->
         IF r = SwapRun THEN "Pending" ELSE "Pending"
    [] OTHER -> "Pending"

VARIABLES
  podState,
  podNode,
  observedNode,
  observedUse,
  machineLive,
  leaseOpen,
  mintCount,
  leaseSlot,
  reservationState,
  runPhase,
  nodeState,
  lastMintWriter,
  lastCloseWriter,
  done,
  step

vars ==
  <<podState, podNode, observedNode, observedUse, machineLive, leaseOpen,
    mintCount, leaseSlot, reservationState, runPhase, nodeState,
    lastMintWriter, lastCloseWriter, done, step>>

Init ==
  /\ podState = [p \in Pods |-> InitialPodState(p)]
  /\ podNode = [p \in Pods |-> InitialPodNode(p)]
  /\ observedNode = [p \in Pods |-> NoNode]
  /\ observedUse = [p \in Pods |-> 0]
  /\ machineLive = {p \in Pods : InitialPodState(p) \in {"Bound", "Terminating"}}
  /\ leaseOpen = [p \in Pods |-> InitialLeaseOpen(p)]
  /\ mintCount = [p \in Pods |-> InitialLeaseOpen(p)]
  /\ leaseSlot = [p \in Pods |-> InitialLeaseSlot(p)]
  /\ reservationState =
       IF InitMode = "Lifecycle" THEN "Due" ELSE "Released"
  /\ runPhase = [r \in Runs |-> InitialRunPhase(r)]
  /\ nodeState = [n \in Nodes |-> InitialNodeState(n)]
  /\ lastMintWriter = "None"
  /\ lastCloseWriter = "None"
  /\ done = FALSE
  /\ step = 0

AttemptExists(p) == podState[p] \notin {"Absent", "Gone"}

\* Bound and terminating pods are physical. Assumed/Permitted/Prebound pods
\* consume scheduler-cache capacity so a later scheduling cycle cannot commit
\* the same allocatable unit before the first Bind completes.
SchedulerCommitted(p) ==
  podState[p] \in {"Assumed", "Permitted", "Prebound", "BindFailed"}

\* The negative knob reconstructs the exact category error under test: treating
\* a closed accounting record as if it made a still-bound/terminating pod stop
\* consuming scheduler capacity.
CountedAtCommit(p) ==
  \/ SchedulerCommitted(p)
  \/ /\ p \in machineLive
     /\ (~ClosedLeaseFreesPhysical \/ leaseOpen[p] > 0)

CommitUse(n) ==
  Cardinality({p \in Pods : CountedAtCommit(p) /\ podNode[p] = n})

PhysicalUse(n) ==
  Cardinality({p \in machineLive : podNode[p] = n})

MayTarget(p, n) ==
  /\ nodeState[n] # "Fenced"
  /\ (p # Swap \/ n = "n2")

EmitDirect ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[Direct] = "Absent"
  /\ (~MaterializationGuard \/ ~AttemptExists(Reserved))
  /\ podState' = [podState EXCEPT ![Direct] = "Pending"]
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, leaseOpen,
                 mintCount, leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, lastCloseWriter, done>>

EmitPeer ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[Peer] = "Absent"
  /\ podState' = [podState EXCEPT ![Peer] = "Pending"]
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, leaseOpen,
                 mintCount, leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, lastCloseWriter, done>>

ActivateReservation ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ reservationState = "Due"
  /\ podState[Reserved] = "Absent"
  /\ (~MaterializationGuard \/ ~AttemptExists(Direct))
  /\ reservationState' = "Released"
  /\ podState' = [podState EXCEPT ![Reserved] = "Pending"]
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, leaseOpen,
                 mintCount, leaseSlot, runPhase, nodeState, lastMintWriter,
                 lastCloseWriter, done>>

Observe(p, n) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Pending"
  /\ MayTarget(p, n)
  \* A mutated swap may trust the controller's planned handoff instead of the
  \* scheduler's real capacity view. Ordinary attempts always pass Filter.
  /\ ((p = Swap /\ ~SwapChecksCapacity) \/ CommitUse(n) < Capacity)
  /\ observedNode' = [observedNode EXCEPT ![p] = n]
  /\ observedUse' = [observedUse EXCEPT ![p] = CommitUse(n)]
  /\ podState' = [podState EXCEPT ![p] = "Observed"]
  /\ UNCHANGED <<podNode, machineLive, leaseOpen, mintCount, leaseSlot,
                 reservationState, runPhase, nodeState, lastMintWriter,
                 lastCloseWriter, done>>

Assume(p) ==
  LET n == observedNode[p] IN
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Observed"
  /\ n \in Nodes
  /\ MayTarget(p, n)
  /\ IF p = Swap
     THEN ~SwapChecksCapacity \/ CommitUse(n) < Capacity
     ELSE IF AtomicCapacityCommit
          THEN CommitUse(n) < Capacity
          ELSE observedUse[p] < Capacity
  /\ podState' = [podState EXCEPT ![p] = "Assumed"]
  /\ podNode' = [podNode EXCEPT ![p] = n]
  /\ UNCHANGED <<observedNode, observedUse, machineLive, leaseOpen, mintCount,
                 leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, lastCloseWriter, done>>

Permit(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Assumed"
  /\ podState' = [podState EXCEPT ![p] = "Permitted"]
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, leaseOpen,
                 mintCount, leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, lastCloseWriter, done>>

\* Scheduler-only mint. A same-pod, same-incarnation retry sees the same lease
\* name. IdempotentPreBind therefore keeps mintCount/open count at one.
PreBind(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Permitted"
  /\ mintCount[p] < 2
  \* A correct retry must not accept an immutable lease minted for another
  \* node. Current production omits this check; the dedicated negative config
  \* reproduces that behavior while keeping the first lease slot frozen.
  /\ (~RetryPlacementGuard
       \/ mintCount[p] = 0
       \/ SlotNode(leaseSlot[p]) = podNode[p])
  /\ podState' = [podState EXCEPT ![p] = "Prebound"]
  /\ mintCount' =
       [mintCount EXCEPT
         ![p] = IF IdempotentPreBind /\ mintCount[p] > 0
                THEN mintCount[p] ELSE mintCount[p] + 1]
  /\ leaseOpen' =
       [leaseOpen EXCEPT
         ![p] = IF IdempotentPreBind /\ leaseOpen[p] > 0
                THEN leaseOpen[p] ELSE leaseOpen[p] + 1]
  /\ leaseSlot' =
       [leaseSlot EXCEPT
         ![p] = IF IdempotentPreBind /\ mintCount[p] > 0
                THEN leaseSlot[p] ELSE SlotForNode(podNode[p])]
  /\ lastMintWriter' = "Scheduler"
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive,
                 reservationState, runPhase, nodeState, lastCloseWriter, done>>

\* The framework can call PreBind again for the same pod after a retry. The
\* positive branch is a stutter on the lease plane; the mutation creates a
\* second lease for the same pod and nonce.
RepeatPreBind(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Prebound"
  /\ mintCount[p] < 2
  /\ mintCount' =
       [mintCount EXCEPT
         ![p] = IF IdempotentPreBind THEN mintCount[p] ELSE mintCount[p] + 1]
  /\ leaseOpen' =
       [leaseOpen EXCEPT
         ![p] = IF IdempotentPreBind THEN leaseOpen[p] ELSE leaseOpen[p] + 1]
  /\ lastMintWriter' = "Scheduler"
  /\ UNCHANGED <<podState, podNode, observedNode, observedUse, machineLive,
                 leaseSlot, reservationState, runPhase, nodeState,
                 lastCloseWriter, done>>

BindSuccess(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Prebound"
  /\ nodeState[podNode[p]] # "Fenced"
  /\ podState' = [podState EXCEPT ![p] = "Bound"]
  /\ machineLive' = machineLive \cup {p}
  /\ runPhase' = [runPhase EXCEPT ![RunOf(p)] = "Running"]
  /\ reservationState' =
       IF p = Direct THEN "Released" ELSE reservationState
  /\ UNCHANGED <<podNode, observedNode, observedUse, leaseOpen, mintCount,
                 leaseSlot, nodeState, lastMintWriter, lastCloseWriter, done>>

BindFailure(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Prebound"
  /\ podState' = [podState EXCEPT ![p] = "BindFailed"]
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, leaseOpen,
                 mintCount, leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, lastCloseWriter, done>>

\* Kubernetes calls every Reserve plugin's Unreserve and forgets the assumed pod
\* after a PreBind/Bind failure. The Lease created by jobtree PreBind remains.
Unreserve(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "BindFailed"
  /\ podState' = [podState EXCEPT ![p] = "Pending"]
  /\ podNode' = [podNode EXCEPT ![p] = NoNode]
  /\ observedNode' = [observedNode EXCEPT ![p] = NoNode]
  /\ observedUse' = [observedUse EXCEPT ![p] = 0]
  /\ UNCHANGED <<machineLive, leaseOpen, mintCount, leaseSlot,
                 reservationState, runPhase, nodeState, lastMintWriter,
                 lastCloseWriter, done>>

\* A scheduler restart loses an unbound assumption. The durable Lease survives,
\* and retry remains idempotent. This abstracts gang reconstruction only enough
\* to preserve that physical/accounting distinction.
SchedulerRestart(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] \in {"Assumed", "Permitted", "Prebound", "BindFailed"}
  /\ podState' = [podState EXCEPT ![p] = "Pending"]
  /\ podNode' = [podNode EXCEPT ![p] = NoNode]
  /\ observedNode' = [observedNode EXCEPT ![p] = NoNode]
  /\ observedUse' = [observedUse EXCEPT ![p] = 0]
  /\ UNCHANGED <<machineLive, leaseOpen, mintCount, leaseSlot,
                 reservationState, runPhase, nodeState, lastMintWriter,
                 lastCloseWriter, done>>

\* Only the controller closes. Closing starts termination for a bound pod but
\* does not remove it from machineLive. A pending post-PreBind orphan can also
\* be closed once the controller has positive evidence to abandon it.
ControllerClose(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ leaseOpen[p] > 0
  /\ podState[p] \in {"Pending", "Bound", "Terminating", "Gone"}
  /\ leaseOpen' = [leaseOpen EXCEPT ![p] = 0]
  /\ podState' =
       [podState EXCEPT
         ![p] = IF podState[p] = "Bound" THEN "Terminating" ELSE podState[p]]
  /\ lastCloseWriter' = "Controller"
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, mintCount,
                 leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, done>>

Terminate(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Bound"
  /\ podState' = [podState EXCEPT ![p] = "Terminating"]
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, leaseOpen,
                 mintCount, leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, lastCloseWriter, done>>

PodGone(p) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[p] = "Terminating"
  /\ podState' = [podState EXCEPT ![p] = "Gone"]
  /\ podNode' = [podNode EXCEPT ![p] = NoNode]
  /\ machineLive' = machineLive \ {p}
  /\ UNCHANGED <<observedNode, observedUse, leaseOpen, mintCount, leaseSlot,
                 reservationState, runPhase, nodeState, lastMintWriter,
                 lastCloseWriter, done>>

\* Fencing is positive evidence that n1's machine-live work is gone. A cordon
\* alone never does this.
FenceFailedNode ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ nodeState["n1"] \in {"Ready", "Cordoned"}
  /\ nodeState' = [nodeState EXCEPT !["n1"] = "Fenced"]
  /\ machineLive' = {p \in machineLive : podNode[p] # "n1"}
  /\ podState' =
       [p \in Pods |->
         IF p \in machineLive /\ podNode[p] = "n1" THEN "Gone"
         ELSE podState[p]]
  /\ podNode' =
       [p \in Pods |->
         IF p \in machineLive /\ podNode[p] = "n1" THEN NoNode
         ELSE podNode[p]]
  /\ UNCHANGED <<observedNode, observedUse, leaseOpen, mintCount, leaseSlot,
                 reservationState, runPhase, lastMintWriter, lastCloseWriter,
                 done>>

Cordon(n) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ nodeState[n] = "Ready"
  /\ nodeState' = [nodeState EXCEPT ![n] = "Cordoned"]
  /\ UNCHANGED <<podState, podNode, observedNode, observedUse, machineLive,
                 leaseOpen, mintCount, leaseSlot, reservationState, runPhase,
                 lastMintWriter, lastCloseWriter, done>>

Uncordon(n) ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ nodeState[n] = "Cordoned"
  /\ nodeState' = [nodeState EXCEPT ![n] = "Ready"]
  /\ UNCHANGED <<podState, podNode, observedNode, observedUse, machineLive,
                 leaseOpen, mintCount, leaseSlot, reservationState, runPhase,
                 lastMintWriter, lastCloseWriter, done>>

\* HandleNodeFailure's logical handoff: the spare and exact-slot blocker leases
\* close through the controller, their pods begin asynchronous termination, and
\* the hard-pinned swap attempt appears. Physical capacity is not free until
\* PodGone removes both terminating pods from machineLive.
PrepareSwap ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  /\ podState[Old] = "Gone"
  /\ podState[Swap] = "Absent"
  /\ podState[Spare] = "Bound"
  /\ podState[Blocker] = "Bound"
  /\ leaseOpen[Spare] > 0
  /\ leaseOpen[Blocker] > 0
  /\ podState' =
       [podState EXCEPT
         ![Spare] = "Terminating",
         ![Blocker] = "Terminating",
         ![Swap] = "Pending"]
  /\ leaseOpen' =
       [leaseOpen EXCEPT ![Spare] = 0, ![Blocker] = 0]
  /\ lastCloseWriter' = "Controller"
  /\ UNCHANGED <<podNode, observedNode, observedUse, machineLive, mintCount,
                 leaseSlot, reservationState, runPhase, nodeState,
                 lastMintWriter, done>>

\* A bounded recovery obligation without fairness. The positive design may call
\* the scenario quiescent only when every charging lease still has physical work
\* behind it. The mutation permits "finished" immediately after Unreserve,
\* exposing an abandoned PreBind lease. This is not a claim that production has
\* a deadline; production correspondence is reported separately.
FinishScenario ==
  /\ InitMode = "Lifecycle"
  /\ ~done
  \* Keep the negative rail on the named seam: a lease was minted at PreBind,
  \* then Unreserve or scheduler restart returned its pod to Pending.
  /\ \E p \in Pods: podState[p] = "Pending" /\ leaseOpen[p] > 0
  /\ IF RequireBindFailureCleanup
     THEN \A p \in Pods: leaseOpen[p] = 0 \/ p \in machineLive
     ELSE TRUE
  /\ done' = TRUE
  /\ UNCHANGED <<podState, podNode, observedNode, observedUse, machineLive,
                 leaseOpen, mintCount, leaseSlot, reservationState, runPhase,
                 nodeState, lastMintWriter, lastCloseWriter>>

CoreNext ==
  \/ EmitDirect
  \/ EmitPeer
  \/ ActivateReservation
  \/ \E p \in Pods, n \in Nodes: Observe(p, n)
  \/ \E p \in Pods: Assume(p)
  \/ \E p \in Pods: Permit(p)
  \/ \E p \in Pods: PreBind(p)
  \/ \E p \in Pods: RepeatPreBind(p)
  \/ \E p \in Pods: BindSuccess(p)
  \/ \E p \in Pods: BindFailure(p)
  \/ \E p \in Pods: Unreserve(p)
  \/ \E p \in Pods: SchedulerRestart(p)
  \/ \E p \in Pods: ControllerClose(p)
  \/ \E p \in Pods: Terminate(p)
  \/ \E p \in Pods: PodGone(p)
  \/ FenceFailedNode
  \/ \E n \in Nodes: Cordon(n) \/ Uncordon(n)
  \/ PrepareSwap
  \/ FinishScenario

Next ==
  /\ step < MaxSteps
  /\ step' = step + 1
  /\ CoreNext

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ podState \in [Pods -> PodStates]
  /\ podNode \in [Pods -> NodesOrNone]
  /\ observedNode \in [Pods -> NodesOrNone]
  /\ observedUse \in [Pods -> 0..Cardinality(Pods)]
  /\ machineLive \subseteq Pods
  /\ leaseOpen \in [Pods -> 0..2]
  /\ mintCount \in [Pods -> 0..2]
  /\ leaseSlot \in [Pods -> SlotsOrNone]
  /\ reservationState \in ReservationStates
  /\ runPhase \in [Runs -> RunPhases]
  /\ nodeState \in [Nodes -> NodeStates]
  /\ lastMintWriter \in Writers
  /\ lastCloseWriter \in Writers
  /\ done \in BOOLEAN
  /\ step \in 0..MaxSteps

MachinePlaneConsistent ==
  \A p \in Pods:
    (p \in machineLive <=> podState[p] \in {"Bound", "Terminating"})

PhysicalCapacitySafe ==
  \A n \in Nodes: PhysicalUse(n) <= Capacity

OneMachineLivePodPerRank ==
  \A r \in {RankOf(p) : p \in Pods}:
    Cardinality({p \in machineLive : RankOf(p) = r}) <= 1

SingleAdmissionMaterialization ==
  Cardinality({p \in AdmissionPods : AttemptExists(p)}) <= 1

TerminatingPodStillHoldsPhysicalCapacity ==
  \A p \in Pods: podState[p] = "Terminating" => p \in machineLive

BoundWorkHasUniqueOpenLease ==
  \A p \in Pods: podState[p] = "Bound" => leaseOpen[p] = 1

BoundLeaseMatchesPodNode ==
  \A p \in Pods:
    podState[p] = "Bound" => SlotNode(leaseSlot[p]) = podNode[p]

OneMintPerPodAndNonce ==
  \A p \in Pods: mintCount[p] <= 1

OnlySchedulerMints ==
  lastMintWriter \in {"None", "Scheduler"}

OnlyControllerCloses ==
  lastCloseWriter \in {"None", "Controller"}

QuiescentHasNoChargingOrphan ==
  done => \A p \in Pods: leaseOpen[p] = 0 \/ p \in machineLive

\* Rejected invariants. Each has a dedicated legal-state config that MUST
\* produce a counterexample. None belongs in PhysicalCapacity.cfg.
NoDuplicateOpenLeaseSlots ==
  \A p, q \in Pods:
    /\ p # q
    /\ leaseOpen[p] > 0
    /\ leaseOpen[q] > 0
    => leaseSlot[p] # leaseSlot[q]

OpenLeaseNodeIsInCapacityView ==
  \A p \in Pods:
    leaseOpen[p] > 0
      => /\ SlotNode(leaseSlot[p]) \in Nodes
         /\ nodeState[SlotNode(leaseSlot[p])] = "Ready"

SpareImpliesOpenActiveLease ==
  leaseOpen[Spare] = 0
    \/ \E p \in SwapRunPods:
         RoleOf(p) = "Active" /\ leaseOpen[p] > 0

RunningImpliesOpenActiveLease ==
  \A r \in Runs:
    runPhase[r] = "Running"
      => \E p \in ActivePods:
           RunOf(p) = r /\ leaseOpen[p] > 0

=============================================================================

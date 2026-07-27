------------------------ MODULE BlockedReservation ------------------------
(***************************************************************************)
(* Temporal companion to ReservationLifecycle.tla for the BlockedFunding   *)
(* state introduced after the frozen-backlog defect.  One due reservation  *)
(* starts with no funding binding and a visible countdown/gauge.  Blocking *)
(* must make the indefinite wait honest and inert; repeated scans preserve *)
(* its original onset; repair wakes it automatically; and a run that no    *)
(* longer needs the reservation releases it.                               *)
(*                                                                          *)
(* The scheduler remains the sole lease minter.  Activation emits exactly  *)
(* one intent gang and the later PluginMint action creates its leases.      *)
(* This model does not pretend the controller/plugin apply is atomic.       *)
(***************************************************************************)

EXTENDS Naturals

CONSTANTS
  \* @type: Bool;
  RevisitBlocked,
  \* @type: Bool;
  PreserveBlockedOnset,
  \* @type: Bool;
  ClearBlockedGauge,
  \* @type: Bool;
  ReleaseBlockedOnSupersede,
  \* @type: Bool;
  SchedulerSoleMinter

ASSUME
  /\ RevisitBlocked \in BOOLEAN
  /\ PreserveBlockedOnset \in BOOLEAN
  /\ ClearBlockedGauge \in BOOLEAN
  /\ ReleaseBlockedOnSupersede \in BOOLEAN
  /\ SchedulerSoleMinter \in BOOLEAN

Pending == "Pending"
BlockedFunding == "BlockedFunding"
Released == "Released"

RunPending == "RunPending"
Scheduling == "Scheduling"
Running == "Running"
Terminal == "Terminal"

NoCause == "NoCause"
NoFunding == "NoFunding"
NoWriter == "NoWriter"
Controller == "Controller"
Scheduler == "Scheduler"

VARIABLES
  \* @type: Str;
  reservation,
  \* @type: Str;
  runPhase,
  \* @type: Bool;
  binding,
  \* @type: Int;
  blockedSince,
  \* @type: Str;
  cause,
  \* @type: Int;
  countdown,
  \* @type: Int;
  gauge,
  \* @type: Int;
  now,
  \* @type: Int;
  emissions,
  \* @type: Int;
  pods,
  \* @type: Int;
  leases,
  \* @type: Str;
  lastLeaseWriter

vars ==
  <<reservation, runPhase, binding, blockedSince, cause, countdown, gauge,
    now, emissions, pods, leases, lastLeaseWriter>>

Init ==
  /\ reservation = Pending
  /\ runPhase = RunPending
  /\ binding = FALSE
  /\ blockedSince = 0
  /\ cause = NoCause
  /\ countdown = 1
  /\ gauge = 1
  /\ now = 1
  /\ emissions = 0
  /\ pods = 0
  /\ leases = 0
  /\ lastLeaseWriter = NoWriter

\* The first due activation sees that no principal can pay.
Block ==
  /\ reservation = Pending
  /\ ~binding
  /\ reservation' = BlockedFunding
  /\ blockedSince' = now
  /\ cause' = NoFunding
  /\ countdown' = 0
  /\ gauge' = IF ClearBlockedGauge THEN 0 ELSE gauge
  /\ UNCHANGED
       <<runPhase, binding, now, emissions, pods, leases, lastLeaseWriter>>

\* BlockedFunding is deliberately included in the activation scan.  While the
\* cause remains, that scan is idempotent except for advancing the model clock.
Reblock ==
  /\ reservation = BlockedFunding
  /\ ~binding
  /\ now < 3
  /\ now' = now + 1
  /\ blockedSince' =
       IF PreserveBlockedOnset THEN blockedSince ELSE now + 1
  /\ cause' = NoFunding
  /\ countdown' = 0
  /\ gauge' = IF ClearBlockedGauge THEN 0 ELSE gauge
  /\ UNCHANGED
       <<reservation, runPhase, binding, emissions, pods, leases,
         lastLeaseWriter>>

\* A repaired binding either reaches the mirrored BlockedFunding wake-up path,
\* or (negative control) remains stuck because that exact string was omitted.
Repair ==
  /\ reservation = BlockedFunding
  /\ ~binding
  /\ binding' = TRUE
  /\ IF RevisitBlocked
     THEN /\ reservation' = Released
          /\ runPhase' = Scheduling
          /\ blockedSince' = 0
          /\ cause' = NoCause
          /\ countdown' = 0
          /\ gauge' = 0
          /\ emissions' = emissions + 1
          /\ pods' = 2
          /\ leases' = IF SchedulerSoleMinter THEN 0 ELSE 2
          /\ lastLeaseWriter' =
               IF SchedulerSoleMinter THEN NoWriter ELSE Controller
     ELSE /\ UNCHANGED
               <<reservation, runPhase, blockedSince, cause, countdown, gauge,
                 emissions, pods, leases, lastLeaseWriter>>
  /\ UNCHANGED now

\* The scheduler plugin's PreBind is the only positive-model lease writer.
PluginMint ==
  /\ runPhase = Scheduling
  /\ pods = 2
  /\ leases = 0
  /\ leases' = 2
  /\ lastLeaseWriter' = Scheduler
  /\ runPhase' = Running
  /\ UNCHANGED
       <<reservation, binding, blockedSince, cause, countdown, gauge, now,
         emissions, pods>>

\* A terminal or independently-bound run no longer needs its reservation.
Supersede ==
  /\ reservation = BlockedFunding
  /\ runPhase = RunPending
  /\ runPhase' = Terminal
  /\ IF ReleaseBlockedOnSupersede
     THEN /\ reservation' = Released
          /\ blockedSince' = 0
          /\ cause' = NoCause
          /\ countdown' = 0
          /\ gauge' = 0
     ELSE /\ UNCHANGED
               <<reservation, blockedSince, cause, countdown, gauge>>
  /\ UNCHANGED
       <<binding, now, emissions, pods, leases, lastLeaseWriter>>

Next == Block \/ Reblock \/ Repair \/ PluginMint \/ Supersede

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ reservation \in {Pending, BlockedFunding, Released}
  /\ runPhase \in {RunPending, Scheduling, Running, Terminal}
  /\ binding \in BOOLEAN
  /\ blockedSince \in 0..3
  /\ cause \in {NoCause, NoFunding}
  /\ countdown \in 0..1
  /\ gauge \in 0..1
  /\ now \in 1..3
  /\ emissions \in 0..1
  /\ pods \in 0..2
  /\ leases \in 0..2
  /\ lastLeaseWriter \in {NoWriter, Controller, Scheduler}

BlockedVisibleAndInert ==
  reservation # BlockedFunding
  \/ /\ cause = NoFunding
     /\ blockedSince > 0
     /\ countdown = 0
     /\ gauge = 0
     /\ emissions = 0
     /\ pods = 0
     /\ leases = 0

OriginalOnsetSurvives ==
  reservation # BlockedFunding \/ binding \/ blockedSince = 1

RepairedDoesNotStayBlocked ==
  ~(reservation = BlockedFunding /\ binding)

NoBlockedReservationForDoneRun ==
  runPhase # Terminal \/ reservation # BlockedFunding

SingleGangEmission == emissions <= 1
LeaseHasPod == leases <= pods
OnlySchedulerMints == leases = 0 \/ lastLeaseWriter = Scheduler

=============================================================================

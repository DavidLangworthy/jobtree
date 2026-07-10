--------------------- MODULE LedgerCompactionAccounting ----------------------
(***************************************************************************)
(* LedgerCompactionAccounting is the broader companion to the existing       *)
(* compaction proofs. The other two specs answer "is compaction locally or   *)
(* statefully correct at all?" This one broadens the accounting surface:     *)
(*                                                                          *)
(* - aggregate-cap history across two envelopes,                             *)
(* - full window identity (`Start` and `End`), and                           *)
(* - class / lender attribution in the compacted summary.                    *)
(*                                                                          *)
(* Why this exists                                                           *)
(* ---------------                                                           *)
(* The earlier proofs were intentionally narrow: one envelope, one scalar    *)
(* settled summary, and a window-start invalidation rule. That proved the    *)
(* settlement and persisted-store contracts, but it did not broaden the      *)
(* modeled customer surface very much. The next three breadth increases we   *)
(* wanted were:                                                              *)
(*                                                                          *)
(* 1. aggregate-cap carry-forward,                                           *)
(* 2. full window identity rather than just `WindowStart`, and               *)
(* 3. class/lender accounting, not just a scalar consumed-hours total.       *)
(*                                                                          *)
(* This module does all three at once on a still-bounded abstraction.        *)
(*                                                                          *)
(* What is modeled                                                           *)
(* ----------------                                                           *)
(* - two payer envelopes, each with its own current window;                  *)
(* - two settled-summary aggregate buckets: one shared cap across both        *)
(*   envelopes and one env-1-only cap, so aggregate history is not merely    *)
(*   the same number as total consumed hours;                                *)
(* - two ranked leases, each assigned to one envelope and either owned or    *)
(*   borrowed;                                                               *)
(* - per-envelope `ConsumedGPUHours`, per-envelope `HoursByClass`,           *)
(*   per-cap aggregate consumed hours, and per-owner lender hours;           *)
(* - a persisted summary store keyed by the full window identity used when   *)
(*   it was computed.                                                        *)
(*                                                                          *)
(* Why the abstraction is still small                                        *)
(* ----------------------------------                                        *)
(* We keep only two leases and unit-width ticks, and we fix them to one      *)
(* representative owned-then-borrowed history. That keeps the SMT encoding   *)
(* tractable while still exercising the three missing surfaces:              *)
(*                                                                          *)
(* - aggregate-cap depletion can differ by envelope membership,              *)
(* - a window-start OR window-end change can stale the summary, and          *)
(* - owned vs borrowed vs unfunded hours can diverge if the summary is       *)
(*   wrong.                                                                  *)
(*                                                                          *)
(* The point here is broader coverage, not a full refinement proof of the    *)
(* funding engine.                                                           *)
(*                                                                          *)
(* One important result from broadening the model                            *)
(* ----------------------------------------                                 *)
(* A naive "just add the newly settled counters onto the old summary" rule   *)
(* is false once the summary carries depletion-sensitive accounting such as   *)
(* envelope consumed hours, aggregate-cap consumed hours, and lender hours.  *)
(* The newly settled epoch must be replayed from the prior summary state,    *)
(* not from zero and then added component-wise. That is why this module      *)
(* proves a seeded fold theorem rather than a plain additive theorem.        *)
(*                                                                          *)
(* The direct universally-quantified seeded-fold encoding remains too large *)
(* for Apalache on a 16 GB VM: a 10 GB heap exhausted during preprocessing, *)
(* and a 12.5 GB run received SIGTERM near the VM limit. Apalache checks    *)
(* the representative 0 -> 1 and 1 -> 2 steps. The exact finite universal   *)
(* operator is checked with TLC by the dedicated SeededFoldUniversal config. *)
(*                                                                          *)
(* Implementation anchors                                                    *)
(* ----------------------                                                    *)
(* - theorem and no-straddle contract:                                       *)
(*   `docs/project/remediation/R4-pt2-ledger-compaction.md:61`              *)
(* - full window identity / invalidation caveat:                             *)
(*   `docs/project/remediation/R4-pt2-ledger-compaction.md:80`              *)
(* - aggregate and lender summary fields deferred to pt2b:                   *)
(*   `docs/project/remediation/R4-pt2-ledger-compaction.md:88`              *)
(* - aggregate caps excluded from pt2a today:                                *)
(*   `pkg/funding/evaluate.go:306`                                           *)
(* - lender hours and class hours in the evaluator:                          *)
(*   `pkg/funding/evaluate.go:811`                                           *)
(*   `pkg/funding/evaluate.go:839`                                           *)
(*                                                                          *)
(* Update this spec when the stored summary contents, aggregate carry-       *)
(* forward, or window invalidation rules change.                             *)
(***************************************************************************)

EXTENDS Naturals, FiniteSets

CONSTANT
  \* @type: Bool;
  InvalidateOnWindowShift

ASSUME InvalidateOnWindowShift \in BOOLEAN

LeaseIds == 1..2
Envs == 1..2
Owners == 1..2
Caps == 1..2
Ticks == 0..2
Boundaries == 0..2
Ends == 1..3
NoEnd == 3

Owned == 1
Borrowed == 2
Unfunded == 3
Inactive == 0
Classes == {Owned, Borrowed, Unfunded}

(* @typeAlias: envHours = Int -> Int;
   @typeAlias: envClassHours = Int -> (Int -> Int);
   @typeAlias: capHours = Int -> Int;
   @typeAlias: lenderHours = Int -> Int;
   @typeAlias: leaseSet = Set(Int);
   @typeAlias: leaseClasses = Int -> Int;
   @typeAlias: stateT = {
     consumed: $envHours,
     classHours: $envClassHours,
     aggregate: $capHours,
     lender: $lenderHours
   };
*)
LedgerCompactionAccounting_aliases == TRUE

VARIABLES
  \* @type: Int;
  now,
  \* @type: Int;
  horizon,
  \* @type: Bool;
  summaryValid,
  \* @type: (Int -> Int);
  windowStart,
  \* @type: (Int -> Int);
  windowEnd,
  \* @type: (Int -> Int);
  summaryWindowStart,
  \* @type: (Int -> Int);
  summaryWindowEnd,
  \* @type: (Int -> Int);
  summaryConsumed,
  \* @type: (Int -> (Int -> Int));
  summaryClassHours,
  \* @type: (Int -> Int);
  summaryAggregate,
  \* @type: (Int -> Int);
  summaryLender,
  \* @type: (Int -> Bool);
  leaseEnabled,
  \* @type: (Int -> Int);
  leaseEnv,
  \* @type: (Int -> Bool);
  leaseBorrowed,
  \* @type: (Int -> Int);
  leaseStart,
  \* @type: (Int -> Int);
  leaseEnd

vars ==
  <<now, horizon, summaryValid, windowStart, windowEnd,
    summaryWindowStart, summaryWindowEnd, summaryConsumed, summaryClassHours,
    summaryAggregate, summaryLender, leaseEnabled, leaseEnv, leaseBorrowed,
    leaseStart, leaseEnd>>

ZeroConsumed == [e \in Envs |-> 0]
ZeroClassHours == [e \in Envs |-> [cl \in Classes |-> 0]]
ZeroAggregate == [c \in Caps |-> 0]
ZeroLender == [o \in Owners |-> 0]

EnvCap(e) == 2
AggCap(c) == IF c = 1 THEN 2 ELSE 1
LenderCap(o) == 1

CapContains(c, e) == (c = 1) \/ (c = 2 /\ e = 1)
OwnerOfEnv(e) == e
ClassOfLease(l) == IF leaseBorrowed[l] THEN Borrowed ELSE Owned

Init ==
  /\ now = 0
  /\ horizon = 0
  /\ summaryValid = FALSE
  /\ windowStart = [e \in Envs |-> 0]
  /\ windowEnd = [e \in Envs |-> NoEnd]
  /\ summaryWindowStart = [e \in Envs |-> 0]
  /\ summaryWindowEnd = [e \in Envs |-> NoEnd]
  /\ summaryConsumed = ZeroConsumed
  /\ summaryClassHours = ZeroClassHours
  /\ summaryAggregate = ZeroAggregate
  /\ summaryLender = ZeroLender
  /\ leaseEnabled = [l \in LeaseIds |-> TRUE]
  /\ leaseEnv = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]
  /\ leaseBorrowed = [l \in LeaseIds |-> l = 2]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 0 ELSE 1]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]

\* Two settled leases accrue before the window moves: an owned env-1 lease and a
\* borrowed env-2 lease. If the caller keeps the summary valid across the window
\* change, the compacted path over-charges the shifted envelope, the shared
\* aggregate, and whichever class/lender bucket that envelope contributed to.
InitStaleWindowWitness ==
  /\ now = 0
  /\ horizon = 0
  /\ summaryValid = FALSE
  /\ windowStart = [e \in Envs |-> 0]
  /\ windowEnd = [e \in Envs |-> NoEnd]
  /\ summaryWindowStart = [e \in Envs |-> 0]
  /\ summaryWindowEnd = [e \in Envs |-> NoEnd]
  /\ summaryConsumed = ZeroConsumed
  /\ summaryClassHours = ZeroClassHours
  /\ summaryAggregate = ZeroAggregate
  /\ summaryLender = ZeroLender
  /\ leaseEnabled = [l \in LeaseIds |-> TRUE]
  /\ leaseEnv = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]
  /\ leaseBorrowed = [l \in LeaseIds |-> l = 2]
  /\ leaseStart = [l \in LeaseIds |-> 0]
  /\ leaseEnd = [l \in LeaseIds |-> 1]

MakeState(consumed, classHours, aggregate, lender) ==
  [consumed |-> consumed,
   classHours |-> classHours,
   aggregate |-> aggregate,
   lender |-> lender]

ZeroState == MakeState(ZeroConsumed, ZeroClassHours, ZeroAggregate, ZeroLender)

Active(l, t) == leaseEnabled[l] /\ leaseStart[l] <= t /\ t < leaseEnd[l]

\* @type: (Int, $stateT, Int -> Int, Int -> Int, Int) => Bool;
Eligible(l, state, starts, ends, t) ==
  /\ Active(l, t)
  /\ starts[leaseEnv[l]] <= t
  /\ t < ends[leaseEnv[l]]
  /\ state.consumed[leaseEnv[l]] < EnvCap(leaseEnv[l])
  /\ \A c \in Caps : CapContains(c, leaseEnv[l]) => state.aggregate[c] < AggCap(c)
  /\ IF leaseBorrowed[l] THEN state.lender[OwnerOfEnv(leaseEnv[l])] < LenderCap(OwnerOfEnv(leaseEnv[l])) ELSE TRUE

\* @type: ($stateT, Int, Int) => $stateT;
ApplyFunded(state, l, class) ==
  LET env == leaseEnv[l]
      owner == OwnerOfEnv(env)
      agg1 == IF CapContains(1, env) THEN [state.aggregate EXCEPT ![1] = @ + 1] ELSE state.aggregate
      agg2 == IF CapContains(2, env) THEN [agg1 EXCEPT ![2] = @ + 1] ELSE agg1
      lender1 == IF class = Borrowed THEN [state.lender EXCEPT ![owner] = @ + 1] ELSE state.lender
  IN
    MakeState(
      [state.consumed EXCEPT ![env] = @ + 1],
      [state.classHours EXCEPT ![env][class] = @ + 1],
      agg2,
      lender1)

\* @type: ($stateT, Int) => $stateT;
ApplyUnfunded(state, l) ==
  LET env == leaseEnv[l]
  IN MakeState(
       state.consumed,
       [state.classHours EXCEPT ![env][Unfunded] = @ + 1],
       state.aggregate,
       state.lender)

\* @type: ($stateT, Int, Int) => $stateT;
ApplyClass(state, l, class) ==
  IF class = Owned \/ class = Borrowed THEN ApplyFunded(state, l, class)
  ELSE IF class = Unfunded THEN ApplyUnfunded(state, l)
  ELSE state

\* @type: ($stateT, Set(Int), Int -> Int, Int -> Int, Int) => Int;
TickClass1(state, S, starts, ends, t) ==
  IF 1 \notin S \/ ~Active(1, t) THEN Inactive
  ELSE IF Eligible(1, state, starts, ends, t) THEN ClassOfLease(1)
  ELSE Unfunded

\* @type: ($stateT, Set(Int), Int -> Int, Int -> Int, Int) => $stateT;
TickState1(state, S, starts, ends, t) ==
  ApplyClass(state, 1, TickClass1(state, S, starts, ends, t))

\* @type: ($stateT, Set(Int), Int -> Int, Int -> Int, Int) => Int;
TickClass2(state, S, starts, ends, t) ==
  LET after1 == TickState1(state, S, starts, ends, t)
  IN IF 2 \notin S \/ ~Active(2, t) THEN Inactive
     ELSE IF Eligible(2, after1, starts, ends, t) THEN ClassOfLease(2)
     ELSE Unfunded

\* @type: ($stateT, Set(Int), Int -> Int, Int -> Int, Int) => $stateT;
TickState(state, S, starts, ends, t) ==
  LET after1 == TickState1(state, S, starts, ends, t)
  IN ApplyClass(after1, 2, TickClass2(state, S, starts, ends, t))

\* @type: (Int, Int, $stateT, Set(Int), Int -> Int, Int -> Int) => $stateT;
StateBetween(start, limit, seed, S, starts, ends) ==
  LET after0 == IF start <= 0 /\ 0 < limit THEN TickState(seed, S, starts, ends, 0) ELSE seed
      after1 == IF start <= 1 /\ 1 < limit THEN TickState(after0, S, starts, ends, 1) ELSE after0
  IN after1

\* @type: (Int, $stateT, Set(Int), Int -> Int, Int -> Int) => $stateT;
StateBefore(limit, seed, S, starts, ends) ==
  StateBetween(0, limit, seed, S, starts, ends)

\* @type: (Int, $stateT, Set(Int), Int -> Int, Int -> Int) => Int -> Int;
ClassesAt(limit, seed, S, starts, ends) ==
  LET base == StateBefore(limit, seed, S, starts, ends)
      c1 == IF 1 \notin S \/ ~Active(1, limit) THEN Inactive
            ELSE IF Eligible(1, base, starts, ends, limit) THEN ClassOfLease(1)
            ELSE Unfunded
      after1 == ApplyClass(base, 1, c1)
      c2 == IF 2 \notin S \/ ~Active(2, limit) THEN Inactive
            ELSE IF Eligible(2, after1, starts, ends, limit) THEN ClassOfLease(2)
            ELSE Unfunded
  IN [l \in LeaseIds |-> IF l = 1 THEN c1 ELSE c2]

SeedState(consumed, classHours, aggregate, lender) ==
  MakeState(consumed, classHours, aggregate, lender)

LeaseSet == {l \in LeaseIds : leaseEnabled[l]}
SettledBy(h) == {l \in LeaseSet : leaseEnd[l] <= h}
RetainedFrom(h) == LeaseSet \ SettledBy(h)
EpochSet(oldH, newH) == {l \in LeaseSet : oldH < leaseEnd[l] /\ leaseEnd[l] <= newH}

NoStraddleAt(h) == \A l \in RetainedFrom(h) : leaseStart[l] >= h
CanCompactAt(h) == h <= now /\ NoStraddleAt(h)

SettledState(h, starts, ends) == StateBefore(h, ZeroState, SettledBy(h), starts, ends)
EpochFoldState(oldH, newH, seed, starts, ends) ==
  StateBetween(oldH, newH, seed, EpochSet(oldH, newH), starts, ends)

ExpectedState1 ==
  MakeState(
    [e \in Envs |-> IF e = 1 THEN 1 ELSE 0],
    [e \in Envs |->
       IF e = 1
       THEN [cl \in Classes |-> IF cl = Owned THEN 1 ELSE 0]
       ELSE [cl \in Classes |-> 0]],
    [c \in Caps |-> 1],
    [o \in Owners |-> 0])

ExpectedState2 ==
  MakeState(
    [e \in Envs |-> 1],
    [e \in Envs |->
       IF e = 1
       THEN [cl \in Classes |-> IF cl = Owned THEN 1 ELSE 0]
       ELSE [cl \in Classes |-> IF cl = Borrowed THEN 1 ELSE 0]],
    [c \in Caps |-> IF c = 1 THEN 2 ELSE 1],
    [o \in Owners |-> IF o = 2 THEN 1 ELSE 0])

ExpectedBorrowedOnlyState ==
  MakeState(
    [e \in Envs |-> IF e = 2 THEN 1 ELSE 0],
    [e \in Envs |->
       IF e = 2
       THEN [cl \in Classes |-> IF cl = Borrowed THEN 1 ELSE 0]
       ELSE [cl \in Classes |-> 0]],
    [c \in Caps |-> IF c = 1 THEN 1 ELSE 0],
    [o \in Owners |-> IF o = 2 THEN 1 ELSE 0])

ExpectedShiftedStartState1 ==
  MakeState(
    [e \in Envs |-> 0],
    [e \in Envs |->
       IF e = 1
       THEN [cl \in Classes |-> IF cl = Unfunded THEN 1 ELSE 0]
       ELSE [cl \in Classes |-> 0]],
    [c \in Caps |-> 0],
    [o \in Owners |-> 0])

ExpectedShiftedStartStateNow ==
  MakeState(
    ExpectedBorrowedOnlyState.consumed,
    [e \in Envs |->
       IF e = 1
       THEN [cl \in Classes |-> IF cl = Unfunded THEN 1 ELSE 0]
       ELSE [cl \in Classes |-> IF cl = Borrowed THEN 1 ELSE 0]],
    ExpectedBorrowedOnlyState.aggregate,
    ExpectedBorrowedOnlyState.lender)

ExpectedShiftedEndState2 ==
  MakeState(
    ExpectedState1.consumed,
    [e \in Envs |->
       IF e = 1
       THEN [cl \in Classes |-> IF cl = Owned THEN 1 ELSE 0]
       ELSE [cl \in Classes |-> IF cl = Unfunded THEN 1 ELSE 0]],
    ExpectedState1.aggregate,
    ExpectedState1.lender)

OneShotInit ==
  /\ now = 2
  /\ horizon = 1
  /\ summaryValid = TRUE
  /\ windowStart = [e \in Envs |-> 0]
  /\ windowEnd = [e \in Envs |-> NoEnd]
  /\ summaryWindowStart = windowStart
  /\ summaryWindowEnd = windowEnd
  /\ summaryConsumed = ExpectedState1.consumed
  /\ summaryClassHours = ExpectedState1.classHours
  /\ summaryAggregate = ExpectedState1.aggregate
  /\ summaryLender = ExpectedState1.lender
  /\ leaseEnabled = [l \in LeaseIds |-> TRUE]
  /\ leaseEnv = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]
  /\ leaseBorrowed = [l \in LeaseIds |-> l = 2]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 0 ELSE 1]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]

OneShotStaleStartWitness ==
  /\ now = 2
  /\ horizon = 1
  /\ summaryValid = TRUE
  /\ windowStart = [e \in Envs |-> IF e = 1 THEN 1 ELSE 0]
  /\ windowEnd = [e \in Envs |-> NoEnd]
  /\ summaryWindowStart = [e \in Envs |-> 0]
  /\ summaryWindowEnd = [e \in Envs |-> NoEnd]
  /\ summaryConsumed = ExpectedState1.consumed
  /\ summaryClassHours = ExpectedState1.classHours
  /\ summaryAggregate = ExpectedState1.aggregate
  /\ summaryLender = ExpectedState1.lender
  /\ leaseEnabled = [l \in LeaseIds |-> TRUE]
  /\ leaseEnv = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]
  /\ leaseBorrowed = [l \in LeaseIds |-> l = 2]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 0 ELSE 1]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]

OneShotStaleEndWitness ==
  /\ now = 2
  /\ horizon = 2
  /\ summaryValid = TRUE
  /\ windowStart = [e \in Envs |-> 0]
  /\ windowEnd = [e \in Envs |-> IF e = 2 THEN 1 ELSE NoEnd]
  /\ summaryWindowStart = [e \in Envs |-> 0]
  /\ summaryWindowEnd = [e \in Envs |-> NoEnd]
  /\ summaryConsumed = ExpectedState2.consumed
  /\ summaryClassHours = ExpectedState2.classHours
  /\ summaryAggregate = ExpectedState2.aggregate
  /\ summaryLender = ExpectedState2.lender
  /\ leaseEnabled = [l \in LeaseIds |-> TRUE]
  /\ leaseEnv = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]
  /\ leaseBorrowed = [l \in LeaseIds |-> l = 2]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 0 ELSE 1]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]

OneShotRepairedStartWitness ==
  /\ now = 2
  /\ horizon = 1
  /\ summaryValid = TRUE
  /\ windowStart = [e \in Envs |-> IF e = 1 THEN 1 ELSE 0]
  /\ windowEnd = [e \in Envs |-> NoEnd]
  /\ summaryWindowStart = windowStart
  /\ summaryWindowEnd = windowEnd
  /\ summaryConsumed = ExpectedShiftedStartState1.consumed
  /\ summaryClassHours = ExpectedShiftedStartState1.classHours
  /\ summaryAggregate = ExpectedShiftedStartState1.aggregate
  /\ summaryLender = ExpectedShiftedStartState1.lender
  /\ leaseEnabled = [l \in LeaseIds |-> TRUE]
  /\ leaseEnv = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]
  /\ leaseBorrowed = [l \in LeaseIds |-> l = 2]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 0 ELSE 1]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]

OneShotRepairedEndWitness ==
  /\ now = 2
  /\ horizon = 2
  /\ summaryValid = TRUE
  /\ windowStart = [e \in Envs |-> 0]
  /\ windowEnd = [e \in Envs |-> IF e = 2 THEN 1 ELSE NoEnd]
  /\ summaryWindowStart = windowStart
  /\ summaryWindowEnd = windowEnd
  /\ summaryConsumed = ExpectedShiftedEndState2.consumed
  /\ summaryClassHours = ExpectedShiftedEndState2.classHours
  /\ summaryAggregate = ExpectedShiftedEndState2.aggregate
  /\ summaryLender = ExpectedShiftedEndState2.lender
  /\ leaseEnabled = [l \in LeaseIds |-> TRUE]
  /\ leaseEnv = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]
  /\ leaseBorrowed = [l \in LeaseIds |-> l = 2]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 0 ELSE 1]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 2]

FullStateNow == StateBefore(now, ZeroState, LeaseSet, windowStart, windowEnd)
FullClassesNow == ClassesAt(now, ZeroState, LeaseSet, windowStart, windowEnd)

CompactionActive == summaryValid /\ CanCompactAt(horizon)
SummarySeed == SeedState(summaryConsumed, summaryClassHours, summaryAggregate, summaryLender)

CompactedStateNow ==
  IF CompactionActive
  THEN StateBefore(now, SummarySeed, RetainedFrom(horizon), windowStart, windowEnd)
  ELSE FullStateNow

CompactedClassesNow ==
  IF CompactionActive
  THEN ClassesAt(now, SummarySeed, RetainedFrom(horizon), windowStart, windowEnd)
  ELSE FullClassesNow

SeededSettlementFold ==
  \A oldH \in Boundaries :
    \A newH \in Boundaries :
      /\ oldH <= newH
      /\ newH <= now
      /\ NoStraddleAt(oldH)
      /\ NoStraddleAt(newH)
      =>
         SettledState(newH, windowStart, windowEnd) =
           EpochFoldState(
             oldH,
             newH,
             SettledState(oldH, windowStart, windowEnd),
             windowStart,
             windowEnd)

RepresentativeSeededSettlementFold ==
  /\ SettledState(1, windowStart, windowEnd) =
       EpochFoldState(
         0,
         1,
         SettledState(0, windowStart, windowEnd),
         windowStart,
         windowEnd)
  /\ SettledState(2, windowStart, windowEnd) =
       EpochFoldState(
         1,
         2,
         SettledState(1, windowStart, windowEnd),
         windowStart,
         windowEnd)

RepresentativeSeededFold01 ==
  /\ SettledState(1, windowStart, windowEnd) = ExpectedState1
  /\ EpochFoldState(0, 1, ZeroState, windowStart, windowEnd) = ExpectedState1

RepresentativeSeededFold12 ==
  /\ SettledState(2, windowStart, windowEnd) = ExpectedState2
  /\ EpochFoldState(1, 2, ExpectedState1, windowStart, windowEnd) = ExpectedState2

RepresentativeCompositionalSettlement ==
  /\ EpochFoldState(0, 2, ZeroState, windowStart, windowEnd) = ExpectedState2
  /\ EpochFoldState(1, 2, EpochFoldState(0, 1, ZeroState, windowStart, windowEnd),
                    windowStart, windowEnd) = ExpectedState2
  /\ EpochFoldState(0, 2, ZeroState, windowStart, windowEnd) =
       EpochFoldState(1, 2, EpochFoldState(0, 1, ZeroState, windowStart, windowEnd),
                      windowStart, windowEnd)

SummaryRep ==
  summaryValid =>
    /\ horizon <= now
    /\ NoStraddleAt(horizon)
    /\ summaryConsumed = SettledState(horizon, summaryWindowStart, summaryWindowEnd).consumed
    /\ summaryClassHours = SettledState(horizon, summaryWindowStart, summaryWindowEnd).classHours
    /\ summaryAggregate = SettledState(horizon, summaryWindowStart, summaryWindowEnd).aggregate
    /\ summaryLender = SettledState(horizon, summaryWindowStart, summaryWindowEnd).lender

StatefulRoundTrip ==
  /\ FullStateNow.consumed = CompactedStateNow.consumed
  /\ FullStateNow.classHours = CompactedStateNow.classHours
  /\ FullStateNow.aggregate = CompactedStateNow.aggregate
  /\ FullStateNow.lender = CompactedStateNow.lender
  /\ FullClassesNow = CompactedClassesNow

ConsumedRoundTrip ==
  FullStateNow.consumed = CompactedStateNow.consumed

ClassHoursRoundTrip ==
  FullStateNow.classHours = CompactedStateNow.classHours

AggregateRoundTrip ==
  FullStateNow.aggregate = CompactedStateNow.aggregate

LenderRoundTrip ==
  FullStateNow.lender = CompactedStateNow.lender

AdvanceNow ==
  /\ now < 2
  /\ now' = now + 1
  /\ UNCHANGED <<horizon, summaryValid, windowStart, windowEnd,
                 summaryWindowStart, summaryWindowEnd, summaryConsumed,
                 summaryClassHours, summaryAggregate, summaryLender,
                 leaseEnabled, leaseEnv, leaseBorrowed, leaseStart, leaseEnd>>

SettleTo(newH) ==
  /\ newH \in Boundaries
  /\ horizon < newH
  /\ CanCompactAt(newH)
  /\ horizon' = newH
  /\ summaryWindowStart' = windowStart
  /\ summaryWindowEnd' = windowEnd
  /\ IF summaryValid /\ summaryWindowStart = windowStart /\ summaryWindowEnd = windowEnd
     THEN LET folded == EpochFoldState(horizon, newH, SummarySeed, windowStart, windowEnd)
          IN /\ summaryConsumed' = folded.consumed
             /\ summaryClassHours' = folded.classHours
             /\ summaryAggregate' = folded.aggregate
             /\ summaryLender' = folded.lender
     ELSE /\ summaryConsumed' = SettledState(newH, windowStart, windowEnd).consumed
          /\ summaryClassHours' = SettledState(newH, windowStart, windowEnd).classHours
          /\ summaryAggregate' = SettledState(newH, windowStart, windowEnd).aggregate
          /\ summaryLender' = SettledState(newH, windowStart, windowEnd).lender
  /\ summaryValid' = TRUE
  /\ UNCHANGED <<now, windowStart, windowEnd, leaseEnabled, leaseEnv,
                 leaseBorrowed, leaseStart, leaseEnd>>

ShiftWindowStart(e, newS) ==
  /\ e \in Envs
  /\ newS \in Ticks
  /\ newS # windowStart[e]
  /\ newS < windowEnd[e]
  /\ newS <= now
  /\ windowStart' = [windowStart EXCEPT ![e] = newS]
  /\ UNCHANGED <<now, horizon, windowEnd, summaryWindowStart, summaryWindowEnd,
                 summaryConsumed, summaryClassHours, summaryAggregate, summaryLender,
                 leaseEnabled, leaseEnv, leaseBorrowed, leaseStart, leaseEnd>>
  /\ IF InvalidateOnWindowShift
     THEN summaryValid' = FALSE
     ELSE UNCHANGED summaryValid

ShiftWindowEnd(e, newE) ==
  /\ e \in Envs
  /\ newE \in Ends
  /\ newE # windowEnd[e]
  /\ windowStart[e] < newE
  /\ windowEnd' = [windowEnd EXCEPT ![e] = newE]
  /\ UNCHANGED <<now, horizon, windowStart, summaryWindowStart, summaryWindowEnd,
                 summaryConsumed, summaryClassHours, summaryAggregate, summaryLender,
                 leaseEnabled, leaseEnv, leaseBorrowed, leaseStart, leaseEnd>>
  /\ IF InvalidateOnWindowShift
     THEN summaryValid' = FALSE
     ELSE UNCHANGED summaryValid

RepairSummary ==
  /\ ~summaryValid
  /\ horizon <= now
  /\ summaryWindowStart' = windowStart
  /\ summaryWindowEnd' = windowEnd
  /\ summaryConsumed' = SettledState(horizon, windowStart, windowEnd).consumed
  /\ summaryClassHours' = SettledState(horizon, windowStart, windowEnd).classHours
  /\ summaryAggregate' = SettledState(horizon, windowStart, windowEnd).aggregate
  /\ summaryLender' = SettledState(horizon, windowStart, windowEnd).lender
  /\ summaryValid' = TRUE
  /\ UNCHANGED <<now, horizon, windowStart, windowEnd, leaseEnabled, leaseEnv,
                 leaseBorrowed, leaseStart, leaseEnd>>

Next ==
  \/ AdvanceNow
  \/ \E h \in Boundaries : SettleTo(h)
  \/ \E e \in Envs : \E s \in Ticks : ShiftWindowStart(e, s)
  \/ \E e \in Envs : \E en \in Ends : ShiftWindowEnd(e, en)
  \/ RepairSummary

Spec == Init /\ [][Next]_vars
StaleWindowSpec == InitStaleWindowWitness /\ [][Next]_vars
OneShotNext == UNCHANGED vars
OneShotSpec == OneShotInit /\ [][OneShotNext]_vars
OneShotStaleStartSpec == OneShotStaleStartWitness /\ [][OneShotNext]_vars
OneShotStaleEndSpec == OneShotStaleEndWitness /\ [][OneShotNext]_vars
OneShotRepairedStartSpec == OneShotRepairedStartWitness /\ [][OneShotNext]_vars
OneShotRepairedEndSpec == OneShotRepairedEndWitness /\ [][OneShotNext]_vars

TypeOK ==
  /\ now \in Ticks
  /\ horizon \in Boundaries
  /\ horizon <= now
  /\ summaryValid \in BOOLEAN
  /\ windowStart \in [Envs -> Ticks]
  /\ windowEnd \in [Envs -> Ends]
  /\ summaryWindowStart \in [Envs -> Ticks]
  /\ summaryWindowEnd \in [Envs -> Ends]
  /\ \A e \in Envs :
       /\ windowStart[e] < windowEnd[e]
       /\ summaryWindowStart[e] < summaryWindowEnd[e]
  /\ summaryConsumed \in [Envs -> Nat]
  /\ summaryClassHours \in [Envs -> [Classes -> Nat]]
  /\ summaryAggregate \in [Caps -> Nat]
  /\ summaryLender \in [Owners -> Nat]
  /\ leaseEnabled \in [LeaseIds -> BOOLEAN]
  /\ leaseEnv \in [LeaseIds -> Envs]
  /\ leaseBorrowed \in [LeaseIds -> BOOLEAN]
  /\ leaseStart \in [LeaseIds -> Ticks]
  /\ leaseEnd \in [LeaseIds -> Ends]
  /\ \A l \in LeaseIds : leaseEnabled[l] => leaseStart[l] < leaseEnd[l]

=============================================================================

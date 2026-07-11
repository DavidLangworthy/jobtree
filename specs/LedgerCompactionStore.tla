----------------------- MODULE LedgerCompactionStore ------------------------
(***************************************************************************)
(* LedgerCompactionStore is the stronger, stateful theorem for R4 pt2b.     *)
(* `LedgerCompaction.tla` proves the local pt2a fact: if one chosen horizon *)
(* is safe, then "summary + retained" matches full replay for that one      *)
(* evaluation.  This module proves the thing the persisted settlement store *)
(* actually needs: repeated settlement, time advance, and window movement   *)
(* must keep the compacted representation observationally equivalent to a   *)
(* fresh replay of the full history.                                        *)
(*                                                                          *)
(* Why this theorem exists                                                  *)
(* ----------------------                                                   *)
(* The risky part of pt2 is not `settlementSafe` by itself; it is the       *)
(* persisted summary store in the budget controller.  A stale or            *)
(* mis-keyed summary can drift from full replay even if a one-shot          *)
(* `SettleAccrual` call is locally correct.  The two caller-side contracts  *)
(* from the design doc are the load-bearing ones here:                      *)
(*                                                                          *)
(* - settlement is compositional: advancing the horizon incrementally must  *)
(*   match replaying the whole settled prefix from scratch, and             *)
(* - window movement must invalidate or recompute the summary before the    *)
(*   compacted path is used again.                                          *)
(*                                                                          *)
(* What this model proves                                                   *)
(* ----------------------                                                   *)
(* 1. `SettlementAdditive`: folding the newly settled epoch into the stored *)
(*    summary matches replaying the whole settled prefix under the same     *)
(*    window.                                                               *)
(* 2. `SummaryRep`: every stored summary state denotes exactly the settled  *)
(*    prefix at the recorded horizon and recorded window.                   *)
(* 3. `StatefulRoundTrip`: after any sequence of                            *)
(*      - `AdvanceNow`,                                                    *)
(*      - `SettleTo`,                                                      *)
(*      - `ShiftWindow`, and                                               *)
(*      - `RepairSummary`,                                                 *)
(*    the compacted replay matches full replay, provided the window-shift   *)
(*    caller contract is respected.                                         *)
(*                                                                          *)
(* Why window invalidation is modeled explicitly                            *)
(* -------------------------------------                                    *)
(* The production evaluator does not currently carry `WindowStart` inside    *)
(* `SettledAccrual`.  That is a caller contract for pt2b, not a property    *)
(* `Evaluate` can infer on its own.  This spec therefore models the         *)
(* contract directly: when the window moves, the caller must invalidate or  *)
(* recompute before using the summary again.  The stale-window bug config    *)
(* disables that invalidation and Apalache finds the expected divergence.   *)
(*                                                                          *)
(* What is intentionally abstracted                                         *)
(* --------------------------------                                         *)
(* - one envelope and one capacity dimension, with unit-width leases;       *)
(* - two ranked leases, which is enough to represent historical             *)
(*   competition and retained-vs-settled overlap;                           *)
(* - a window start only (no window end), because the refunding hazard is   *)
(*   "move Start forward and release pre-window hours";                     *)
(* - no aggregate caps yet.  The design doc explicitly defers them to pt2b. *)
(*                                                                          *)
(* Implementation anchors                                                   *)
(* ----------------------                                                   *)
(* - settlement theorem / caller contract:                                  *)
(*   `docs/project/remediation/R4-pt2-ledger-compaction.md:61`              *)
(* - window invalidation caveat:                                            *)
(*   `docs/project/remediation/R4-pt2-ledger-compaction.md:80`              *)
(* - pt2b budget-controller store responsibilities:                         *)
(*   `docs/project/remediation/R4-pt2-ledger-compaction.md:122`             *)
(* - current `settlementSafe` guard:                                        *)
(*   `pkg/funding/evaluate.go:305`                                          *)
(* - current `SettleAccrual` primitive:                                     *)
(*   `pkg/funding/evaluate.go:346`                                          *)
(* - refund behavior on window movement:                                    *)
(*   `pkg/funding/evaluate_test.go:461`                                     *)
(*                                                                          *)
(* Update this spec when the persisted settlement semantics, window         *)
(* invalidation rule, or summary contents change.                           *)
(***************************************************************************)

EXTENDS Naturals, FiniteSets

CONSTANT
  \* @type: Bool;
  InvalidateOnWindowShift

ASSUME InvalidateOnWindowShift \in BOOLEAN

Capacity == 1
LeaseIds == 1..2
Ticks == 0..3
Boundaries == 0..4
NoLeases == {l \in LeaseIds : FALSE}

VARIABLES
  \* @type: Int;
  now,
  \* @type: Int;
  windowStart,
  \* @type: Int;
  horizon,
  \* @type: Bool;
  summaryValid,
  \* @type: Int;
  summaryWindowStart,
  \* @type: Int;
  summaryHours,
  \* @type: (Int -> Bool);
  leaseEnabled,
  \* @type: (Int -> Int);
  leaseStart,
  \* @type: (Int -> Int);
  leaseEnd

vars ==
  <<now, windowStart, horizon, summaryValid, summaryWindowStart, summaryHours,
    leaseEnabled, leaseStart, leaseEnd>>

Init ==
  /\ now = 0
  /\ windowStart = 0
  /\ horizon = 0
  /\ summaryValid = FALSE
  /\ summaryWindowStart = 0
  /\ summaryHours = 0
  /\ leaseEnabled \in [LeaseIds -> BOOLEAN]
  /\ leaseStart \in [LeaseIds -> Ticks]
  /\ leaseEnd \in [LeaseIds -> 1..4]
  /\ \A l \in LeaseIds : leaseEnabled[l] => leaseStart[l] < leaseEnd[l]
  /\ \A l \in LeaseIds :
       ~leaseEnabled[l] =>
         /\ leaseStart[l] = 0
         /\ leaseEnd[l] = 1

\* One closed lease accrues before the window shift. If the caller keeps the
\* summary valid across the shift, the compacted replay over-charges history.
InitWindowShiftWitness ==
  /\ now = 0
  /\ windowStart = 0
  /\ horizon = 0
  /\ summaryValid = FALSE
  /\ summaryWindowStart = 0
  /\ summaryHours = 0
  /\ leaseEnabled = [l \in LeaseIds |-> l = 1]
  /\ leaseStart = [l \in LeaseIds |-> 0]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 1]

LeaseSet == {l \in LeaseIds : leaseEnabled[l]}

ActiveAt(S, t) ==
  {l \in S : leaseStart[l] <= t /\ t < leaseEnd[l]}

FundedAt(S, t) ==
  LET active == ActiveAt(S, t)
  IN IF 1 \in active THEN {1}
     ELSE IF 2 \in active THEN {2}
     ELSE NoLeases

TickHours(S, limit, w, t) ==
  IF t < limit /\ w <= t
  THEN (IF 1 \in FundedAt(S, t) THEN 1 ELSE 0) + (IF 2 \in FundedAt(S, t) THEN 1 ELSE 0)
  ELSE 0

ReplayHours(S, limit, w) ==
  TickHours(S, limit, w, 0) +
  TickHours(S, limit, w, 1) +
  TickHours(S, limit, w, 2) +
  TickHours(S, limit, w, 3)

LeaseTickHours(S, limit, w, l, t) ==
  IF t < limit /\ w <= t /\ l \in FundedAt(S, t) THEN 1 ELSE 0

ReplayLeaseHours(S, limit, w, l) ==
  LeaseTickHours(S, limit, w, l, 0) +
  LeaseTickHours(S, limit, w, l, 1) +
  LeaseTickHours(S, limit, w, l, 2) +
  LeaseTickHours(S, limit, w, l, 3)

SettledBy(h) == {l \in LeaseSet : leaseEnd[l] <= h}
RetainedFrom(h) == LeaseSet \ SettledBy(h)
EpochSet(oldH, newH) == {l \in LeaseSet : oldH < leaseEnd[l] /\ leaseEnd[l] <= newH}

NoStraddleAt(h) == \A l \in RetainedFrom(h) : leaseStart[l] >= h
CanCompactAt(h) == h <= now /\ NoStraddleAt(h)

SettledSummary(h, w) == ReplayHours(SettledBy(h), h, w)
EpochSummary(oldH, newH, w) == ReplayHours(EpochSet(oldH, newH), newH, w)

CompactionActive == summaryValid /\ CanCompactAt(horizon)

FullHoursNow == ReplayHours(LeaseSet, now, windowStart)
CompactedHoursNow ==
  IF CompactionActive
  THEN summaryHours + ReplayHours(RetainedFrom(horizon), now, windowStart)
  ELSE FullHoursNow

RetainedLeaseHoursAgree ==
  \A l \in RetainedFrom(horizon) :
    ReplayLeaseHours(LeaseSet, now, windowStart, l) =
      ReplayLeaseHours(RetainedFrom(horizon), now, windowStart, l)

\* The additive theorem behind persisted settlement. Under one fixed window,
\* folding just the newly settled epoch is enough to recover the whole settled
\* prefix exactly.
SettlementAdditive ==
  \A oldH \in Boundaries :
    \A newH \in Boundaries :
      /\ oldH <= newH
      /\ newH <= now
      /\ NoStraddleAt(oldH)
      /\ NoStraddleAt(newH)
      =>
        SettledSummary(newH, windowStart) =
          SettledSummary(oldH, windowStart) + EpochSummary(oldH, newH, windowStart)

\* If the stored summary is marked valid, it must describe exactly the settled
\* prefix under the window it was computed with.
SummaryRep ==
  summaryValid =>
    /\ summaryHours = SettledSummary(horizon, summaryWindowStart)
    /\ horizon <= now
    /\ NoStraddleAt(horizon)

\* This is the pt2b theorem: the persisted compacted representation is
\* observationally equivalent to replaying the full history.
StatefulRoundTrip ==
  /\ FullHoursNow = CompactedHoursNow
  /\ (CompactionActive => RetainedLeaseHoursAgree)

AdvanceNow ==
  /\ now < 3
  /\ now' = now + 1
  /\ UNCHANGED <<windowStart, horizon, summaryValid, summaryWindowStart, summaryHours,
                 leaseEnabled, leaseStart, leaseEnd>>

SettleTo(newH) ==
  /\ newH \in Boundaries
  /\ horizon < newH
  /\ CanCompactAt(newH)
  /\ horizon' = newH
  /\ summaryHours' =
       IF summaryValid /\ summaryWindowStart = windowStart
       THEN summaryHours + EpochSummary(horizon, newH, windowStart)
       ELSE SettledSummary(newH, windowStart)
  /\ summaryWindowStart' = windowStart
  /\ summaryValid' = TRUE
  /\ UNCHANGED <<now, windowStart, leaseEnabled, leaseStart, leaseEnd>>

ShiftWindow(newW) ==
  /\ newW \in Ticks
  /\ windowStart < newW
  /\ newW <= now
  /\ windowStart' = newW
  /\ IF InvalidateOnWindowShift
     THEN /\ summaryValid' = FALSE
          /\ UNCHANGED <<summaryWindowStart, summaryHours>>
     ELSE UNCHANGED <<summaryValid, summaryWindowStart, summaryHours>>
  /\ UNCHANGED <<now, horizon, leaseEnabled, leaseStart, leaseEnd>>

RepairSummary ==
  /\ ~summaryValid
  /\ horizon <= now
  /\ summaryHours' = SettledSummary(horizon, windowStart)
  /\ summaryWindowStart' = windowStart
  /\ summaryValid' = TRUE
  /\ UNCHANGED <<now, windowStart, horizon, leaseEnabled, leaseStart, leaseEnd>>

Next ==
  \/ AdvanceNow
  \/ \E h \in Boundaries : SettleTo(h)
  \/ \E w \in Ticks : ShiftWindow(w)
  \/ RepairSummary

Spec == Init /\ [][Next]_vars
StaleWindowSpec == InitWindowShiftWitness /\ [][Next]_vars

TypeOK ==
  /\ now \in Ticks
  /\ windowStart \in Ticks
  /\ windowStart <= now
  /\ horizon \in Boundaries
  /\ horizon <= now
  /\ summaryValid \in BOOLEAN
  /\ summaryWindowStart \in Ticks
  /\ summaryHours \in Nat
  /\ leaseEnabled \in [LeaseIds -> BOOLEAN]
  /\ leaseStart \in [LeaseIds -> Ticks]
  /\ leaseEnd \in [LeaseIds -> 1..4]
  /\ \A l \in LeaseIds : leaseEnabled[l] => leaseStart[l] < leaseEnd[l]

=============================================================================

-------------------------- MODULE LedgerCompaction --------------------------
(***************************************************************************)
(* LedgerCompaction is a bounded TLA+ model for the R4 pt2 settlement       *)
(* theorem in `pkg/funding/evaluate.go`.  The production code's contract is *)
(* "full replay" = "settled summary + retained replay" exactly when the     *)
(* settlement horizon is not ahead of the clock and no retained lease       *)
(* starts before the horizon.                                               *)
(*                                                                          *)
(* This is not a full refinement proof of `funding.Evaluate`.  It is a      *)
(* design-level proof obligation for the seam that has been error-prone:    *)
(* dropping old history while preserving the replay result.  The stateful    *)
(* persisted-store theorem lives in `LedgerCompactionStore.tla`; this file   *)
(* stays focused on the one-shot `settlementSafe` contract.                  *)
(*                                                                          *)
(* What is modeled                                                          *)
(* ----------------                                                          *)
(* - one envelope, one greedy capacity dimension, finite discrete time;     *)
(* - leases with start, effective end, and width;                           *)
(* - replayed funded lease-hours and the funded set at `Now`;               *)
(* - a compacted replay that drops settled leases and seeds a scalar        *)
(*   summary of the settled epoch.                                          *)
(*                                                                          *)
(* What is intentionally abstracted                                         *)
(* --------------------------------                                         *)
(* - one budget envelope only;                                              *)
(* - no aggregate caps (matching pt2a's `settlementSafe` guard);            *)
(* - no lending/family graph;                                               *)
(* - discrete ticks instead of continuous timestamps and floating hours.    *)
(*                                                                          *)
(* Even with those abstractions, the two load-bearing correctness holes     *)
(* from the implementation remain representable:                            *)
(*                                                                          *)
(* - a RETAINED lease that straddles the settlement horizon changes its     *)
(*   pre-horizon funding if the settled competitors are dropped;            *)
(* - a horizon ahead of `Now` can treat a still-live lease as settled,      *)
(*   dropping it from the current funded set and over-integrating history.  *)
(*                                                                          *)
(* Implementation anchors                                                   *)
(* ----------------------                                                   *)
(* - theorem and safety condition:                                          *)
(*   `docs/project/remediation/R4-pt2-ledger-compaction.md:61`              *)
(* - compaction inputs and default-off behavior:                            *)
(*   `pkg/funding/evaluate.go:35`                                           *)
(* - settled-lease drop on replay:                                          *)
(*   `pkg/funding/evaluate.go:264`                                          *)
(* - settlementSafe:                                                        *)
(*   `pkg/funding/evaluate.go:299`                                          *)
(* - SettleAccrual:                                                         *)
(*   `pkg/funding/evaluate.go:343`                                          *)
(* - round-trip tests:                                                      *)
(*   `pkg/funding/evaluate_test.go:168`                                     *)
(*                                                                          *)
(* Update this spec when the compaction semantics in those files change.    *)
(***************************************************************************)

EXTENDS Naturals, FiniteSets

CONSTANT
  \* @type: Int;
  Capacity

ASSUME Capacity \in 1..3

LeaseIds == 1..3
Ticks == 0..3
Boundaries == 0..4

VARIABLES
  \* @type: Int;
  now,
  \* @type: Int;
  horizon,
  \* @type: (Int -> Bool);
  leaseEnabled,
  \* @type: (Int -> Int);
  leaseStart,
  \* @type: (Int -> Int);
  leaseEnd,
  \* @type: (Int -> Int);
  leaseWidth

vars == <<now, horizon, leaseEnabled, leaseStart, leaseEnd, leaseWidth>>

Init ==
  /\ now \in Ticks
  /\ horizon \in Boundaries
  /\ leaseEnabled \in [LeaseIds -> BOOLEAN]
  /\ leaseStart \in [LeaseIds -> Ticks]
  /\ leaseEnd \in [LeaseIds -> 1..4]
  /\ leaseWidth \in [LeaseIds -> 1..2]
  /\ \A l \in LeaseIds : leaseEnabled[l] => leaseStart[l] < leaseEnd[l]
  /\ \A l \in LeaseIds :
       ~leaseEnabled[l] =>
         /\ leaseStart[l] = 0
         /\ leaseEnd[l] = 1
         /\ leaseWidth[l] = 1

\* A concrete straddle witness: lease 1 is settled by the horizon, lease 2 is
\* retained but started before the horizon.  With capacity 1, replaying lease 2
\* without its historical competitor changes the accrued history.
InitStraddle ==
  /\ Init
  /\ now = 3
  /\ horizon = 2
  /\ leaseEnabled = [l \in LeaseIds |-> l = 1 \/ l = 2]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 0 ELSE IF l = 2 THEN 1 ELSE 0]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 2 ELSE IF l = 2 THEN 4 ELSE 1]
  /\ leaseWidth = [l \in LeaseIds |-> 1]

\* A concrete future-horizon witness: the horizon leads the clock, so lease 1 is
\* still live at `now` but classes as settled and disappears from the compacted
\* current state.
InitFutureHorizon ==
  /\ Init
  /\ now = 2
  /\ horizon = 3
  /\ leaseEnabled = [l \in LeaseIds |-> l = 1]
  /\ leaseStart = [l \in LeaseIds |-> IF l = 1 THEN 1 ELSE 0]
  /\ leaseEnd = [l \in LeaseIds |-> IF l = 1 THEN 3 ELSE 1]
  /\ leaseWidth = [l \in LeaseIds |-> 1]

Next == UNCHANGED vars

Spec == Init /\ [][Next]_vars
StraddleSpec == InitStraddle /\ [][Next]_vars
FutureHorizonSpec == InitFutureHorizon /\ [][Next]_vars

LeaseSet == {l \in LeaseIds : leaseEnabled[l]}

TotalWidth(S) ==
  (IF 1 \in S THEN leaseWidth[1] ELSE 0) +
  (IF 2 \in S THEN leaseWidth[2] ELSE 0) +
  (IF 3 \in S THEN leaseWidth[3] ELSE 0)

ActiveAt(S, t) ==
  {l \in S : leaseStart[l] <= t /\ t < leaseEnd[l]}

Feasible(S) == TotalWidth(S) <= Capacity

NoLeases == {l \in LeaseIds : FALSE}

CandidateSet(i) ==
  IF i = 1 THEN {1, 2, 3}
  ELSE IF i = 2 THEN {1, 2}
  ELSE IF i = 3 THEN {1, 3}
  ELSE IF i = 4 THEN {1}
  ELSE IF i = 5 THEN {2, 3}
  ELSE IF i = 6 THEN {2}
  ELSE IF i = 7 THEN {3}
  ELSE NoLeases

ValidCandidate(active, candidate) ==
  candidate \subseteq active /\ Feasible(candidate)

FundedAt(S, t) ==
  LET active == ActiveAt(S, t)
  IN IF ValidCandidate(active, CandidateSet(1)) THEN CandidateSet(1)
     ELSE IF ValidCandidate(active, CandidateSet(2)) THEN CandidateSet(2)
     ELSE IF ValidCandidate(active, CandidateSet(3)) THEN CandidateSet(3)
     ELSE IF ValidCandidate(active, CandidateSet(4)) THEN CandidateSet(4)
     ELSE IF ValidCandidate(active, CandidateSet(5)) THEN CandidateSet(5)
     ELSE IF ValidCandidate(active, CandidateSet(6)) THEN CandidateSet(6)
     ELSE IF ValidCandidate(active, CandidateSet(7)) THEN CandidateSet(7)
     ELSE CandidateSet(8)

TickHours(S, limit, t) ==
  IF t < limit THEN TotalWidth(FundedAt(S, t)) ELSE 0

ReplayHours(S, limit) ==
  TickHours(S, limit, 0) +
  TickHours(S, limit, 1) +
  TickHours(S, limit, 2) +
  TickHours(S, limit, 3)

LeaseTickHours(S, limit, l, t) ==
  IF t < limit /\ l \in FundedAt(S, t) THEN leaseWidth[l] ELSE 0

ReplayLeaseHours(S, limit, l) ==
  LeaseTickHours(S, limit, l, 0) +
  LeaseTickHours(S, limit, l, 1) +
  LeaseTickHours(S, limit, l, 2) +
  LeaseTickHours(S, limit, l, 3)

Settled == {l \in LeaseSet : leaseEnd[l] <= horizon}
Retained == LeaseSet \ Settled

NoStraddle == \A l \in Retained : leaseStart[l] >= horizon
SafeCompaction == horizon <= now /\ NoStraddle

\* This is the abstract `SettleAccrual`: replay the settled epoch as of the
\* horizon, then seed the compacted replay from that scalar result.
SettledSummaryHours == ReplayHours(Settled, horizon)

FullHours == ReplayHours(LeaseSet, now)
CompactedHours == SettledSummaryHours + ReplayHours(Retained, now)

FullFundedNow == FundedAt(LeaseSet, now)
CompactedFundedNow == FundedAt(Retained, now)

RetainedLeaseHoursAgree ==
  \A l \in Retained : ReplayLeaseHours(LeaseSet, now, l) = ReplayLeaseHours(Retained, now, l)

ForcedEquivalent ==
  /\ FullHours = CompactedHours
  /\ FullFundedNow = CompactedFundedNow
  /\ RetainedLeaseHoursAgree

SafeRoundTrip ==
  SafeCompaction => ForcedEquivalent

StraddleWitness ==
  /\ ~SafeCompaction
  /\ \E l \in Retained : leaseStart[l] < horizon
  /\ ~ForcedEquivalent

FutureHorizonWitness ==
  /\ horizon > now
  /\ ~ForcedEquivalent

TypeOK ==
  /\ now \in Ticks
  /\ horizon \in Boundaries
  /\ leaseEnabled \in [LeaseIds -> BOOLEAN]
  /\ leaseStart \in [LeaseIds -> Ticks]
  /\ leaseEnd \in [LeaseIds -> 1..4]
  /\ leaseWidth \in [LeaseIds -> 1..2]
  /\ \A l \in LeaseIds : leaseEnabled[l] => leaseStart[l] < leaseEnd[l]

=============================================================================

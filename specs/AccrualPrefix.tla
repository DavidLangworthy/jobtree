---------------------------- MODULE AccrualPrefix ----------------------------
(***************************************************************************)
(* A smallest-known-bad model for owner Ruling 6: elapsed GPU-hour facts do *)
(* not change when funding inputs change later.                              *)
(*                                                                          *)
(* This model intentionally contains two evaluators over the SAME mutations: *)
(*                                                                          *)
(* - SnapshotAfter replays every hour under the latest snapshot, matching    *)
(*   the production evaluator inherited from baseline d736ea4. It must       *)
(*   violate prefix                                                         *)
(*   immutability for conflict onset, delayed Start, reduced MaxGPUHours,    *)
(*   and window rotation.                                                    *)
(* - PersistedAfter keeps every tuple ending at or before the mutation time  *)
(*   and applies the new inputs only afterward. It is the desired P3 shape,  *)
(*   not a claim that the production settlement store exists.                *)
(*                                                                          *)
(* This is a zero-step comparison, not a temporal settlement model: Init     *)
(* selects one mutation and Next stutters. It does not execute Go's          *)
(* funding.Evaluate. Fixed Go specimens calibrate selected outputs, but no   *)
(* state-by-state refinement seam is claimed.                                *)
(*                                                                          *)
(* Hour h denotes the half-open interval [h-1, h). All timestamps are        *)
(* nonzero by type. Recorded adjustments are absent from MutationKinds, so   *)
(* they are excluded by type rather than by a caller-selected Boolean.       *)
(* Window rotation is NOT excluded: old hours retain their old epoch while  *)
(* the new epoch starts with a fresh integral.                               *)
(***************************************************************************)

EXTENDS Naturals, FiniteSets

CONSTANT
  \* @type: Bool;
  KeyIncludesEpoch

ASSUME KeyIncludesEpoch \in BOOLEAN

Conflict == "Conflict"
DelayStart == "DelayStart"
ReduceCap == "ReduceCap"
RotateWindow == "RotateWindow"
MutationKinds == {Conflict, DelayStart, ReduceCap, RotateWindow}

Funded == 1
Unfunded == 0

Hours == 2..5
MutationTime == 4
PrefixHours == {h \in Hours : h <= MutationTime}

VARIABLE
  \* @type: Str;
  mutation

vars == mutation

\* @typeAlias: specT = { bound: Bool, start: Int, cap: Int, epoch: Int };
\* @typeAlias: tupleT = { payer: Int, class: Int, epoch: Int, charge: Int };
AccrualPrefix_aliases == TRUE

BeforeSpec ==
  [bound |-> TRUE, start |-> 1, cap |-> 4, epoch |-> 1]

\* @type: (Str) => $specT;
AfterSpec(kind) ==
  CASE kind = Conflict ->
         [bound |-> FALSE, start |-> 1, cap |-> 4, epoch |-> 1]
    [] kind = DelayStart ->
         [bound |-> TRUE, start |-> 5, cap |-> 4, epoch |-> 1]
    [] kind = ReduceCap ->
         [bound |-> TRUE, start |-> 1, cap |-> 1, epoch |-> 1]
    [] OTHER ->
         [bound |-> TRUE, start |-> 4, cap |-> 4, epoch |-> 2]

\* An hour is inside a window when the window was open at its left boundary.
\* @type: ($specT, Int) => Bool;
Eligible(spec, h) ==
  spec.bound /\ spec.start <= h - 1

\* @type: ($specT, Int) => Int;
EligibleBefore(spec, h) ==
  Cardinality({p \in Hours : p < h /\ Eligible(spec, p)})

\* @type: ($specT, Int) => Bool;
FundedUnder(spec, h) ==
  Eligible(spec, h) /\ EligibleBefore(spec, h) < spec.cap

\* @type: ($specT, Int) => $tupleT;
TupleUnder(spec, h) ==
  IF FundedUnder(spec, h)
  THEN [payer |-> 1, class |-> Funded, epoch |-> spec.epoch, charge |-> 1]
  ELSE [payer |-> 0, class |-> Unfunded, epoch |-> 0, charge |-> 0]

\* @type: (Int) => $tupleT;
BeforeTuple(h) == TupleUnder(BeforeSpec, h)

\* Current production shape: apply the latest snapshot to the whole history.
\* @type: (Str, Int) => $tupleT;
SnapshotAfter(kind, h) == TupleUnder(AfterSpec(kind), h)

\* Desired persisted-prefix shape: only post-mutation hours see the mutation.
\* @type: (Str, Int) => $tupleT;
PersistedAfter(kind, h) ==
  IF h <= MutationTime THEN BeforeTuple(h) ELSE SnapshotAfter(kind, h)

Init ==
  mutation \in MutationKinds

InitConflict ==
  mutation = Conflict

InitDelayStart ==
  mutation = DelayStart

InitReduceCap ==
  mutation = ReduceCap

InitRotateWindow ==
  mutation = RotateWindow

Next == UNCHANGED vars

Spec == Init /\ [][Next]_vars
ConflictSpec == InitConflict /\ [][Next]_vars
DelayStartSpec == InitDelayStart /\ [][Next]_vars
ReduceCapSpec == InitReduceCap /\ [][Next]_vars
RotateWindowSpec == InitRotateWindow /\ [][Next]_vars

TypeOK ==
  /\ mutation \in MutationKinds
  /\ BeforeSpec.start > 0
  /\ MutationTime > BeforeSpec.start
  /\ PrefixHours # {}
  /\ \A h \in Hours :
       /\ BeforeTuple(h).payer \in 0..1
       /\ BeforeTuple(h).class \in 0..1
       /\ BeforeTuple(h).epoch \in 0..2
       /\ BeforeTuple(h).charge \in 0..1

\* The known-bad evaluator must rewrite at least one elapsed tuple for every
\* mutation. Keeping this in the positive config prevents a vacuous persisted
\* theorem over mutations that never exercise the defect.
ExpectedSnapshotRewrite ==
  \E h \in PrefixHours : SnapshotAfter(mutation, h) # BeforeTuple(h)

SnapshotPrefixImmutable ==
  \A h \in PrefixHours : SnapshotAfter(mutation, h) = BeforeTuple(h)

PersistedPrefixImmutable ==
  \A h \in PrefixHours : PersistedAfter(mutation, h) = BeforeTuple(h)

OldWindowCharge ==
  Cardinality({h \in PrefixHours : BeforeTuple(h).charge = 1})

\* A summary keyed only by envelope carries old-window history into epoch 2.
\* Including the epoch makes the renewed window's seed empty.
NewWindowSeed ==
  IF KeyIncludesEpoch THEN 0 ELSE OldWindowCharge

NewWindowFresh ==
  mutation # RotateWindow \/ NewWindowSeed = 0

=============================================================================

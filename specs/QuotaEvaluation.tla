---------------------------- MODULE QuotaEvaluation ----------------------------
(***************************************************************************)
(* The derived funding classification from quota-semantics.md (Decision   *)
(* 3): funded vs. opportunistic is a pure ranked greedy fill over the     *)
(* claims on an envelope — owner first, then borrowers by proximity tier, *)
(* stable admission-order tiebreak within a tier.  Nothing is stored;     *)
(* every state's classification is recomputed from the claim set.        *)
(*                                                                        *)
(* Checked properties:                                                    *)
(*   NoOverdraft          — funded widths never exceed the envelope cap.  *)
(*   OwnerRecall          — an owner claim's funding never depends on     *)
(*                          borrower claims: owners evaluated against     *)
(*                          owners alone get the same answer ("you can    *)
(*                          never be locked out of your own budget").     *)
(*                                                                        *)
(* Stability ("equal claims never reshuffle") is structural: a claim's    *)
(* class depends only on claims ranked above it, and ranks are            *)
(* (tier, admission index) — adding or removing a claim never reorders    *)
(* the survivors.  The integral (GPU-hours) dimension is a second budget  *)
(* filled by the same walk; it is elided to keep the state space tiny.    *)
(***************************************************************************)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS Capacity,   \* envelope concurrency cap
          MaxClaims,  \* bound on live claims
          Widths,     \* claim widths to draw from
          Tiers       \* proximity tiers: 1 = owner, higher = more distant

VARIABLES claims      \* sequence (admission order) of [tier, width] records

vars == <<claims>>

Init == claims = <<>>

AddClaim ==
    /\ Len(claims) < MaxClaims
    /\ \E t \in Tiers, w \in Widths :
         claims' = Append(claims, [tier |-> t, width |-> w])

RemoveClaim ==
    /\ claims # <<>>
    /\ \E i \in DOMAIN claims :
         claims' = SubSeq(claims, 1, i - 1) \o SubSeq(claims, i + 1, Len(claims))

Next == AddClaim \/ RemoveClaim

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(* The ranking: lower tier first; within a tier, admission order.  *)
RankedAbove(i) ==
    { j \in DOMAIN claims \ {i} :
        \/ claims[j].tier < claims[i].tier
        \/ (claims[j].tier = claims[i].tier /\ j < i) }

RECURSIVE SumWidths(_)
SumWidths(S) ==
    IF S = {} THEN 0
    ELSE LET x == CHOOSE y \in S : TRUE
         IN claims[x].width + SumWidths(S \ {x})

(* Greedy fill restricted to a candidate index set: claim i is funded iff *)
(* its width fits after every funded claim ranked above it.               *)
RECURSIVE FundedIn(_, _)
FundedIn(i, Pool) ==
    SumWidths({ j \in RankedAbove(i) \cap Pool : FundedIn(j, Pool) })
        + claims[i].width <= Capacity

IsFunded(i) == FundedIn(i, DOMAIN claims)

Owners == { i \in DOMAIN claims : claims[i].tier = 1 }

-----------------------------------------------------------------------------
NoOverdraft ==
    SumWidths({ i \in DOMAIN claims : IsFunded(i) }) <= Capacity

OwnerRecall ==
    \A i \in Owners : IsFunded(i) <=> FundedIn(i, Owners)
=============================================================================

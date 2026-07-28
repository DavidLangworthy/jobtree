--------------------------- MODULE GrantAuthority ---------------------------
(***************************************************************************)
(* A bounded pure-state model for the P5/P7 grant rules.  It deliberately  *)
(* does not choose where authenticated grants are stored.  Authenticated    *)
(* is an abstract predicate supplied by an authority root; either of the    *)
(* unresolved storage designs may implement that predicate.                 *)
(*                                                                          *)
(* The finite domain contains two delegation branches at depths 1..4.  The  *)
(* checks cover:                                                            *)
(*                                                                          *)
(* - an unauthenticated outsider claim cannot change another namespace's   *)
(*   binding or conflict state (grant locality);                            *)
(* - one owner cannot bind two namespaces, even when the duplicate is in a *)
(*   different delegation subtree (universal injectivity);                 *)
(* - an Owned classification always names the workload's own namespace;    *)
(* - two locally-valid leaf allocations cannot jointly exceed their shared *)
(*   ancestor's instantaneous cap.                                         *)
(*                                                                          *)
(* The four Boolean constants are negative-control mutations.  They are    *)
(* obligations on a future authority/persisted-settlement implementation,  *)
(* not claims that the reviewed production snapshot already implements it. *)
(***************************************************************************)

EXTENDS Naturals, FiniteSets

CONSTANTS
  \* @type: Bool;
  AuthenticateClaims,
  \* @type: Bool;
  UniversalInjectivity,
  \* @type: Bool;
  EnforceAncestorCaps,
  \* @type: Bool;
  KeySponsoredByPayer

ASSUME
  /\ AuthenticateClaims \in BOOLEAN
  /\ UniversalInjectivity \in BOOLEAN
  /\ EnforceAncestorCaps \in BOOLEAN
  /\ KeySponsoredByPayer \in BOOLEAN

Locality == "Locality"
Injectivity == "Injectivity"
Conservation == "Conservation"
Sponsorship == "Sponsorship"
Scenarios == {Locality, Injectivity, Conservation, Sponsorship}

Depths == 1..4
Namespaces == 1..2
Branches == 1..2
Root == 1
Outsider == 6
NoOwner == 0

VARIABLES
  \* @type: Str;
  scenario,
  \* @type: Int;
  depth

vars == <<scenario, depth>>

Init ==
  /\ scenario \in Scenarios
  /\ depth \in Depths

InitLocality ==
  /\ scenario = Locality
  /\ depth \in Depths

InitInjectivity ==
  /\ scenario = Injectivity
  /\ depth \in Depths

InitConservation ==
  /\ scenario = Conservation
  /\ depth \in Depths

InitSponsorship ==
  /\ scenario = Sponsorship
  /\ depth \in Depths

Next == UNCHANGED vars

Spec == Init /\ [][Next]_vars
LocalitySpec == InitLocality /\ [][Next]_vars
InjectivitySpec == InitInjectivity /\ [][Next]_vars
ConservationSpec == InitConservation /\ [][Next]_vars
SponsorshipSpec == InitSponsorship /\ [][Next]_vars

\* Principal depth+1 is the authenticated leaf of the selected chain.
LeafPrincipal == depth + 1

\* The chain is rooted at principal 1 and reaches exactly depth edges.
TrustedPrincipals == 1..LeafPrincipal
Authenticated(p) == p \in TrustedPrincipals

\* Before the outsider mutation, namespace 1 has one rooted binding.
BeforeOwners(ns) ==
  IF ns = 1 THEN {LeafPrincipal} ELSE {}

\* In its own namespace 2, the outsider self-asserts namespace 1's owner.  A
\* trusted authority ignores the unrooted claim.  The historical self-naming
\* shape accepts it, then its cluster-wide injectivity check poisons BOTH the
\* outsider and the otherwise-unchanged victim namespace.
AfterOwners(ns) ==
  IF ns = 1
  THEN BeforeOwners(ns)
  ELSE IF AuthenticateClaims \/ Authenticated(Outsider)
       THEN {}
       ELSE {LeafPrincipal}

ResolvedOwner(owners) ==
  IF Cardinality(owners) = 1 THEN CHOOSE owner \in owners : TRUE ELSE NoOwner

BeforeOwner == ResolvedOwner(BeforeOwners(1))
BeforeConflict == Cardinality(BeforeOwners(1)) > 1

AfterDuplicate(ns) ==
  \E other \in Namespaces :
    /\ other # ns
    /\ ResolvedOwner(AfterOwners(ns)) # NoOwner
    /\ ResolvedOwner(AfterOwners(ns)) = ResolvedOwner(AfterOwners(other))

AfterOwner ==
  IF UniversalInjectivity /\ AfterDuplicate(1)
  THEN NoOwner
  ELSE ResolvedOwner(AfterOwners(1))

AfterConflict == AfterDuplicate(1)

GrantLocality ==
  scenario # Locality
  \/ /\ AfterOwner = BeforeOwner
     /\ AfterConflict = BeforeConflict

\* The injectivity specimen has two individually well-formed rooted grants of
\* the same owner into distinct namespaces.  Universal validation fails both
\* bindings closed; a namespace-local exemption accepts both.
LocalOwner(ns) ==
  IF ns \in Namespaces THEN LeafPrincipal ELSE NoOwner

DuplicateOwner(ns) ==
  \E other \in Namespaces :
    /\ other # ns
    /\ LocalOwner(other) = LocalOwner(ns)
    /\ LocalOwner(ns) # NoOwner

GlobalOwner(ns) ==
  IF UniversalInjectivity /\ DuplicateOwner(ns)
  THEN NoOwner
  ELSE LocalOwner(ns)

OwnerInjective ==
  scenario # Injectivity
  \/ \A left, right \in Namespaces :
       left = right
       \/ GlobalOwner(left) = NoOwner
       \/ GlobalOwner(right) = NoOwner
       \/ GlobalOwner(left) # GlobalOwner(right)

\* A workload in namespace 1 must not become Owned merely because the same
\* owner string was also accepted for the payer in namespace 2.
RunNamespace == 1
PayerNamespace == 2
ClassIsOwned ==
  /\ GlobalOwner(RunNamespace) # NoOwner
  /\ GlobalOwner(RunNamespace) = GlobalOwner(PayerNamespace)

OwnedIsLocal ==
  scenario # Injectivity
  \/ ~ClassIsOwned
  \/ RunNamespace = PayerNamespace

\* Each branch ends in one width-1 request.  Its local envelope cap is 1, but
\* both branches share a root cap of 1.  Depth selects paths of one through
\* four grant edges; the ancestor obligation is identical at every depth.
\* @type: (Int) => Set(Int);
Path(branch) ==
  {branch * 10 + level : level \in {d \in Depths : d <= depth}}

LeafRequests == Branches
FundedRequests ==
  IF EnforceAncestorCaps THEN {1} ELSE LeafRequests

SubtreeWidth == Cardinality(FundedRequests)
RootCap == 1

SubtreeConservation ==
  scenario # Conservation
  \/ SubtreeWidth <= RootCap

\* A sponsored unit is consumed by the borrower but paid from the lender's
\* envelope.  It traverses and charges the lender's ancestors exactly once;
\* keying on the consuming namespace would also charge the borrower lineage.
LenderLineageCharge == 1
BorrowerLineageCharge ==
  IF KeySponsoredByPayer THEN 0 ELSE 1

SponsoredConsumptionIsPayerLocal ==
  scenario # Sponsorship
  \/ /\ LenderLineageCharge = 1
     /\ BorrowerLineageCharge = 0

TypeOK ==
  /\ scenario \in Scenarios
  /\ depth \in Depths
  /\ LeafPrincipal \in 2..5
  /\ Outsider \notin TrustedPrincipals
  /\ \A branch \in Branches : Cardinality(Path(branch)) = depth
  /\ FundedRequests \subseteq LeafRequests

\* The positive model must retain concrete witnesses for every mutation.  If
\* these cease to be true, a green invariant may only mean the defect became
\* unrepresentable.
ExpectedNegativeControls ==
  /\ LET untrusted == [ns \in Namespaces |->
                         IF ns = 1 THEN {LeafPrincipal} ELSE {LeafPrincipal}]
     IN \E left, right \in Namespaces :
          /\ left # right
          /\ ResolvedOwner(untrusted[left]) = ResolvedOwner(untrusted[right])
  /\ \E left, right \in Namespaces :
       /\ left # right
       /\ LocalOwner(left) = LocalOwner(right)
       /\ LocalOwner(left) # NoOwner
  /\ Cardinality(LeafRequests) > RootCap
  /\ LenderLineageCharge = 1
  /\ IF KeySponsoredByPayer
     THEN BorrowerLineageCharge = 0
     ELSE BorrowerLineageCharge = 1

=============================================================================

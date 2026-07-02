------------------------- MODULE ReservationLifecycle -------------------------
(***************************************************************************)
(* One run races its own reservation through plan / direct-bind /         *)
(* activate.  The double-bind defect from the July 2026 design review     *)
(* (R9) is reachable when GuardEnabled = FALSE: a run that reserves, then *)
(* binds directly once capacity frees, leaves a live Pending reservation  *)
(* that materializes a second set of pods/leases on the activation tick.  *)
(* With GuardEnabled = TRUE (the shipped fix: direct bind releases the    *)
(* reservation, activation skips Running runs) the invariants hold.       *)
(***************************************************************************)
EXTENDS Naturals

CONSTANT GuardEnabled

VARIABLES phase,            \* "Pending" | "Running"
          reservation,      \* "None" | "Pending" | "Released"
          materializations, \* times pods/leases were created for the run
          capacity          \* TRUE when the cluster can host the run

vars == <<phase, reservation, materializations, capacity>>

Init ==
    /\ phase = "Pending"
    /\ reservation = "None"
    /\ materializations = 0
    /\ capacity \in BOOLEAN

\* The reconciler cannot place the run and forecasts a reservation.
Reserve ==
    /\ phase = "Pending"
    /\ reservation = "None"
    /\ capacity = FALSE
    /\ reservation' = "Pending"
    /\ UNCHANGED <<phase, materializations, capacity>>

\* Capacity frees at any time (other runs end, nodes return).
CapacityFrees ==
    /\ capacity = FALSE
    /\ capacity' = TRUE
    /\ UNCHANGED <<phase, reservation, materializations>>

\* A later reconcile binds directly.  With the guard, the same step
\* releases any pending reservation (invariant 8).
DirectBind ==
    /\ phase = "Pending"
    /\ capacity = TRUE
    /\ phase' = "Running"
    /\ materializations' = materializations + 1
    /\ reservation' = IF GuardEnabled /\ reservation = "Pending"
                      THEN "Released"
                      ELSE reservation
    /\ UNCHANGED capacity

\* The activation tick fires for a due Pending reservation.  With the
\* guard, a Running run's reservation is released without materializing.
Activate ==
    /\ reservation = "Pending"
    /\ IF phase = "Running"
       THEN IF GuardEnabled
            THEN /\ reservation' = "Released"
                 /\ UNCHANGED <<phase, materializations, capacity>>
            ELSE /\ materializations' = materializations + 1
                 /\ reservation' = "Released"
                 /\ UNCHANGED <<phase, capacity>>
       ELSE /\ capacity = TRUE
            /\ phase' = "Running"
            /\ materializations' = materializations + 1
            /\ reservation' = "Released"
            /\ UNCHANGED capacity

Next == Reserve \/ CapacityFrees \/ DirectBind \/ Activate

Spec == Init /\ [][Next]_vars

\* A run's pods and leases are only ever created once.
SingleMaterialization == materializations <= 1

\* Invariant 8 of the testing plan.
NoPendingReservationForRunningRun ==
    ~(phase = "Running" /\ reservation = "Pending")
=============================================================================

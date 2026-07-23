# Autopilot control

Live steering for the unattended run (`hack/codespace-autopilot.sh`). The autopilot
`git fetch`es and re-reads **this file from `origin/main`** at the start of every item, so
whatever is here on `main` takes effect on its next turn — no restart needed.

**To redirect it:** edit this file, commit, and get it onto `main` (push or merge a PR).
Keep directives short and imperative. Clear them when they no longer apply.

## Active directives

_(none — work the playbook's normal priority order)_

<!--
Examples:
- SKIP R13/R14 for now — I want to schedule the rename outage myself.
- DO R26 (ledger auditor) NEXT, ahead of the conventions items.
- STOP after your current item and wait — I'm about to push a change.
- R4 pt1b staleness bound is now: informer maxAge = 5s. It is UNPARKED; implement it.
-->

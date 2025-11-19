# Budget allocation UX for researchers

Researchers should not need to understand every CRD detail to request or adjust capacity. This
spec outlines a user experience that makes budgeting approachable while still mapping cleanly to
Budgets, Reservations, and Leases.

## 1. User journeys

1. **Request a new allocation**
   * Fill a guided form (or CLI wizard) describing project, GPU flavor, desired concurrency, window,
     and optional lending/borrowing flags.
   * The system suggests envelopes based on existing family budgets and highlights expected headroom.
   * Submission creates a Budget draft (CRD) routed to approvers.

2. **Extend or shrink an allocation**
   * From the dashboard, researchers view active envelopes with utilization history.
   * They can request adjustments; the platform previews the effect on family DAG headroom.

3. **Plan a large run**
   * While filling run details, the UX displays a cost calculator referencing relevant envelopes and
     any sponsor budgets.
   * If the run cannot start now, it immediately shows the reservation countdown and the probability
     of cuts (leveraging forecasting APIs).

## 2. Surfaces

### Web dashboard

* Built with React + PatternFly (future work) backed by the controller APIs.
* Key views:
  * **Home:** upcoming reservations, active runs, recent lotteries.
  * **Budgets:** tree view of family DAG with inline utilization charts.
  * **Requests:** workflow approvals with audit trail.
  * **Runs:** submit, monitor, voluntary shrink.

### CLI enhancements

* `kubectl runs budgets request` launches an interactive TUI using `bubbletea`.
* `kubectl runs budgets approve <request>` for maintainers.
* JSON schema for requests lives under `docs/product/schemas/budget-request.json` (TBD).

### API endpoints

Expose REST endpoints via the controller manager (secured via authz middleware):

* `POST /api/v1/budget-requests`
* `GET /api/v1/budget-requests/:id`
* `POST /api/v1/budget-requests/:id/approve`
* `GET /api/v1/budget-dag` — aggregated headroom and utilization snapshot.

## 3. Accessibility

* Provide inline explanations for terms like “envelope” and “integral cap”.
* Support dark mode and keyboard navigation in the dashboard.
* Localize labels and number formatting (consider i18n from the start).

## 4. Notifications

* Email and Slack notifications when requests are approved or when headroom is within 10% of the
  limit.
* Integrate with the notifier controller to reuse countdown and deficit forecasts.

## 5. Next steps

* Prototype the budget request workflow in Figma and link screenshots in this doc.
* Implement the API backing using Kubernetes CRDs + admission webhooks for validation.
* Add end-to-end tests covering the request → approval → Budget creation path.
* Gather feedback from early researcher cohorts and iterate.

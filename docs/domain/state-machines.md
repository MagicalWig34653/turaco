# Lifecycle & State Machines v0.1

**Status:** Canonical baseline

State represents business meaning. Independent concerns have independent state dimensions. Important transitions are explicit operations and are audited; do not expose generic `UpdateStatus` for core entities.

## Asset lifecycle
`ordered → received → available → reserved → assigned → returned → available`

Branches: `assigned → in_repair → available`, `available/in_service → retired → disposed`, exceptional `lost`.

Provisioning is separate: `not_required | not_started | pending | in_progress | ready | failed`.

Endpoint management is separate: `unmanaged | enrollment_pending | managed | management_stale | management_error | retired`.

Assignment creates/closes historical AssetAssignment records. `disposed` is normally terminal.

## Service Request
`draft → submitted → pending_approval? → approved → in_fulfillment ↔ waiting → completed`

Terminal alternatives: `rejected`, `cancelled`. `waiting_reason` is separate (`stock`, `supplier`, `requester`, `external_system`). Completion requires CatalogItem-defined fulfillment criteria.

## Approval
`pending → approved | rejected | cancelled | expired`. Decisions are immutable; changed decisions create a new approval/correction record.

## Ticket
`new → open → in_progress ↔ waiting → resolved → closed`, with `cancelled` alternative. Waiting reason is separate (`customer`, `vendor`, `external_service`, `scheduled_change`, `hardware`). Reopen is explicit, permissioned, reasoned and audited.

## Major Incident
`identified → investigating → mitigating → monitoring → resolved → closed`.

## Problem
`new → under_investigation → cause_identified → known_error → resolution_planned → resolved → closed`.

## Reservation
`active → fulfilled | released | expired | cancelled`. Terminal reservations are never rewound; create a new reservation.

## Purchase Order
`draft → approved → sent → acknowledged? → partially_received → received → closed`, with `cancelled` where supplier state allows. Posted Goods Receipt is immutable; corrections use reversal/correction transactions.

## Task
`open → in_progress | blocked | completed | cancelled`; `blocked → open/in_progress`; completed reopen is explicit and audited. Assignment is not state.

## Change
`draft → assessment → pending_approval? → approved → scheduled → in_progress → completed → review? → closed`.

Terminal/exception branches: `rejected`, `failed`, `cancelled`. Emergency change can use an abbreviated explicit policy path; it does not bypass audit.

## Initiative
`idea → planning → proposed → approved → active ↔ on_hold → completed`, with `cancelled` alternatives.

## Deployment
Overall: `draft → scheduled? → resolving_targets → ready → running → completed | completed_with_errors | failed | cancelled`.

A few failed devices normally produce `completed_with_errors`, not overall `failed`.

DeploymentTarget: `pending → queued → running → successful | failed | expired`, plus `not_applicable/cancelled`. Retries create immutable DeploymentAttempt history.

## Knowledge Article
`draft → review → published → needs_review → review`, or `archived`.

## Security Advisory / Finding
Advisory: `new → analyzing → applicable | not_applicable → remediation_planned → remediating → resolved → archived`.

Finding: `open → investigating/accepted → remediation_planned → remediating → remediated`, or `false_positive` / `risk_accepted` (reason, actor and review/expiry where policy requires).

## Notification / Agent command
NotificationDelivery: `pending → sending → delivered | failed | cancelled`.

AgentCommand: `created → queued → delivered → acknowledged → running → successful | failed | expired | cancelled`. Expired commands never execute; duplicate delivery never causes duplicate effect.

## Transition requirements

Each core transition defines:
- allowed source state;
- explicit operation and permission;
- guards/invariants;
- required reason where relevant;
- audit record;
- emitted domain event(s);
- deterministic automatic side effects.

Automated transitions use actor=`system`, record the rule/trigger and preserve correlation ID.

## State vs workflow

State machine answers "what states may this entity legally enter?". Workflow answers "what process sequence should happen?". Configurable workflows orchestrate domain operations but do not redefine core state semantics.

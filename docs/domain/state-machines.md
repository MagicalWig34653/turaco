# Lifecycle & State Machines v0.1

**Status:** Canonical baseline

State represents business meaning. Independent concerns have independent state dimensions. Important transitions are explicit operations and are audited; do not expose generic `UpdateStatus` for core entities.

## User status
`active | inactive | departed | external | unknown`, with `status_source` (`platform` | `directory`) recording which side last set the status.

Implemented transitions (F1 slice 3, directory sync only; see the [sync design](../integrations/ldap-ad-sync-design.md#5-sync-algorithm)):
- `active → inactive` when the User has no enabled, still-observed directory identity; sets `status_source = directory`.
- `inactive → active` only when `status_source = directory` and a directory identity is enabled again.
- Sync never sets or leaves `departed`, `external` or `unknown`, and never reactivates an `inactive` User whose `status_source` is `platform`.

Invariants every current and future status operation must keep:
- A platform-side status change sets `status_source = platform`, so directory sync does not undo it.
- Leaving `active` revokes all of the User's sessions in the same transaction (`authentication.RevokeUserSessions`). Sessions are honoured only while the User is `active`.
- Each transition is audited (`organization.user.status_changed`).

In the Organization repository all status changes go through one operation that applies these rules.

## Platform job
`pending → processing → completed`; `processing → pending` (retryable error, with backoff); `processing → failed` (permanent error or attempts exhausted; terminal); `pending → cancelled`. A `processing` job whose lock is older than the runner's lock timeout is reclaimed by another worker. A job interrupted by worker shutdown returns to `pending` without consuming its attempt. Handlers must be idempotent. At most one `pending`/`processing` job exists per dedupe key. See `backend/internal/platform/jobs` and ADR-0006.

## Directory sync run
`running → succeeded | failed | sweep_withheld`. At most one `running` run per provider. A `running` run older than `LDAP_SYNC_TIMEOUT` is marked `failed` (abandoned) by the next run and can no longer commit. A retried job starts a new run.

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

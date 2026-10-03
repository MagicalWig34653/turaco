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

## Role Assignment
`active → revoked` (terminal; re-granting creates a new assignment). At most one active assignment per role, subject and scope. A Role is `active → deleted` (soft) only without active assignments; the built-in role is never deleted. Assignments, revocations and role changes are audited.

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

Implemented (F4, `modules/assets`): an asset exists once goods are received (`ordered` is not a status); manual registration starts in `available` or `received`. Explicit operations and where they start: `make_available` (received, returned), `reserve` / `release_reservation` / `assign_reserved` (only through the Inventory contract; available→reserved, reserved→available, reserved→assigned), `assign` (available), `reassign` (assigned→assigned, closes the old assignment), `return` (assigned→returned), `send_to_repair` (available, returned, assigned; closes an assignment; reason), `finish_repair` (in_repair→available), `retire` (available, returned; reason), `dispose` (retired→disposed; reason; terminal), `mark_lost` (any live status except retired; closes an assignment; reason), `recover` (lost→available; reason). A reason is stored while the asset is in_repair, retired, disposed or lost. At most one assignment is active per asset (partial unique index); every operation is audited and emits `AssetAssigned` (assign, reassign), `AssetReturned` (return) or `AssetStatusChanged`.

## Service Request
`draft → submitted → pending_approval? → approved → in_fulfillment ↔ waiting → completed`

Terminal alternatives: `rejected`, `cancelled`. `waiting_reason` is separate (`stock`, `supplier`, `requester`, `external_system`). Completion requires CatalogItem-defined fulfillment criteria.

Implemented (F3, `modules/requests`): a request is created when submitted (there is no persisted `draft`). Without approval steps it enters `in_fulfillment` at once; with them it is `pending_approval` at step 0. Approving the last step starts fulfillment in the consumer of the `ApprovalDecided` event (`approved` is transient; the consumer runs in the dispatcher's claim transaction and retries transient failures, and a later step nobody eligible could decide rejects the request with the cause `no_eligible_approver`): the catalog's task templates are created as Tasks with typed context `service_request`, and a request without templates completes immediately. Approval steps are sequential and requested one at a time; a rejection ends the request as `rejected`. A request completes automatically when every mandatory task is completed and every optional task is completed or cancelled; a cancelled mandatory task blocks automatic completion and a manager completes the request manually with a reason. Operations: `cancel` (requester while approval is pending, `requests.manage` any time before a terminal state; cancels pending approvals and unfinished tasks), `hold` (`in_fulfillment → waiting` with a reason code), `resume`, `complete` (manual). The requester, the requested-for User, every User named in a `user` answer and everyone who decided an earlier step can never decide an approval of the request. Events: `ServiceRequestSubmitted`, `ServiceRequestApproved`, `ServiceRequestRejected`, `ServiceRequestCompleted`, `ServiceRequestCancelled`.

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

Implemented (F4, `modules/inventory`): `release` (stock becomes available again / the asset becomes `available`) and `fulfill` (stock is issued / the asset is assigned) from `active`; `expired` and `cancelled` are not produced yet (no expiry, and cancelling by origin arrives with the request integration). Reserving checks availability atomically; an asset has at most one active reservation.

## Purchase Order
`draft → approved → sent → acknowledged? → partially_received → received → closed`, with `cancelled` where supplier state allows. Posted Goods Receipt is immutable; corrections use reversal/correction transactions.

## Task
`open → in_progress | blocked | completed | cancelled`; `blocked → open/in_progress`; completed reopen is explicit and audited. Assignment is not state.

Implemented operations (F2, `modules/tasks`): `start` (open/blocked → in_progress), `block(reason)` (open/in_progress → blocked), `unblock` (blocked → open), `complete` (open/in_progress → completed), `cancel(reason)` (open/in_progress/blocked → cancelled), `reopen(reason)` (completed/cancelled → open). Completed and cancelled tasks accept no other change (no edit, no assignment). A reason is stored while a task is blocked or cancelled and cleared on leaving that state; `completed_at` is set exactly while completed. `start`, `block`, `unblock` and `complete` need `tasks.manage` or `tasks.work` on a task assigned to the caller or one of the caller's Teams; `cancel`, `reopen`, editing and assignment need `tasks.manage`. The reasons given for `block`, `cancel` and `reopen` are written to the audit log (they are free text, up to 500 characters, and audit records cannot be redacted; titles and descriptions are never audited). Every change increments `version` (`expectedVersion` is required to edit a task and optional on the other operations; it guards against lost updates), is audited and emits `TaskAssigned` (assignment), `TaskCompleted` (complete) and `TaskCancelled` (cancel, including cancellation of all tasks of a context record by its owning module).

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

## Briefing Item
`draft → published → withdrawn`. Operations (F2, `modules/briefing`): `publish` (draft → published; an expired draft cannot be published; emits `BriefingItemPublished`), `withdraw` (published → withdrawn). Only drafts can be edited or deleted; published and withdrawn items are immutable history, so what readers saw stays traceable (correct a published item by withdrawing it and creating a new one). Viewers (`briefing.view`) see published, unexpired items only; managers (`briefing.manage`) see all. Plain text; titles and bodies are never audited.

## Recurring Task Definition
`active ↔ paused`, plus deletion. `pause` clears the next run; `resume` schedules the first run after now (runs missed while paused are not generated). Changing the rule of an active definition reschedules it from now. The generation job creates at most one Task per definition per pass, for the oldest due run, and moves the schedule to the first run after now.

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

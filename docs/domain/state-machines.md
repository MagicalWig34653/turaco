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

Implemented (F5, `modules/servicedesk`): one ticket kind `incident`. `new` becomes `open` when assigned; operations `start` (new/open → in_progress, assigns the caller when nobody is), `wait(reason)` (open/in_progress → waiting), `resume`, `resolve(resolution)` (any open status), `close` (resolved), `reopen(reason)` (resolved/closed → open) and `cancel(reason)` (before resolution). `tickets.manage` may do all; the reporter and the affected User may close, reopen and cancel while the ticket is new or open. A public reply from the reporter on a ticket waiting for the customer resumes it. Closed and cancelled tickets accept no comments. Events: `TicketCreated`, `TicketAssigned`, `TicketResolved`, `TicketCommentAdded`.

## Knowledge Article
`draft → published → retired`, and `retired → published` (republish). Editing is possible in `draft` and `published`; a retired article is read-only until republished. Visibility: employee articles are readable by every signed-in user once published, internal ones need `knowledge.view`, drafts and retired articles need `knowledge.manage`. A review step (`review`, `needs_review`) from the original target design is not implemented.

## Major Incident
`identified → investigating → mitigating → monitoring → resolved → closed`.

Implemented (F5, `modules/servicedesk`): operations `investigate` (from identified), `mitigate` (identified, investigating), `monitor` (investigating, mitigating), `resolve` (any active status; a message is required) and `close` (resolved). Every step and every `PostUpdate` writes a public timeline entry and replaces the public summary. Subscribing and linking tickets are only possible while the incident is active.

## Problem
`new → under_investigation → cause_identified → known_error → resolution_planned → resolved → closed`.

Implemented (F5 part 2): operations `investigate` (new), `identify_cause(cause)` (new, under_investigation), `mark_known_error(workaround)` (cause_identified), `plan_resolution` (known_error), `resolve(resolution)` (any status before resolved) and `close` (resolved). The database refuses a known error without cause and workaround. A Known Error is a Problem in `known_error` or `resolution_planned`; there is no separate record.

## Reservation
`active → fulfilled | released | expired | cancelled`. Terminal reservations are never rewound; create a new reservation.

Implemented (F4, `modules/inventory`): `release` (stock becomes available again / the asset becomes `available`) and `fulfill` (stock is issued / the asset is assigned) from `active`; `expired` and `cancelled` are not produced yet (no expiry, and cancelling by origin arrives with the request integration). Reserving checks availability atomically; an asset has at most one active reservation.

## Purchase Order
`draft → approved → sent → acknowledged? → partially_received → received → closed`, with `cancelled` where supplier state allows. Posted Goods Receipt is immutable; corrections use reversal/correction transactions.

Implemented (F4, `modules/procurement`): `submit` (draft → pending_approval, one approver User or Team, creator and submitter excluded), approval via the Approvals module (approved → `approved`; rejected → back to `draft` with the reason `approval_rejected`), `send` (approved → sent), `acknowledge` (sent → acknowledged), receipts booked by Goods Receipt (sent, acknowledged or partially_received → `partially_received` or `received` when every line is complete), `close` (received; or partially_received with a reason, which reopens the procurement requests of undelivered lines) and `cancel` (draft, pending_approval, approved, sent or acknowledged, reason required; reopens linked requests, cancels a pending approval). Lines are edited in `draft` only. A Procurement Request goes `open → ordered` when a line takes it, `ordered → fulfilled` when that line is fully received, back to `open` when the order is cancelled or closed short or the line removed, and `open → cancelled` by hand.

## Task
`open → in_progress | blocked | completed | cancelled`; `blocked → open/in_progress`; completed reopen is explicit and audited. Assignment is not state.

Implemented operations (F2, `modules/tasks`): `start` (open/blocked → in_progress), `block(reason)` (open/in_progress → blocked), `unblock` (blocked → open), `complete` (open/in_progress → completed), `cancel(reason)` (open/in_progress/blocked → cancelled), `reopen(reason)` (completed/cancelled → open). Completed and cancelled tasks accept no other change (no edit, no assignment). A reason is stored while a task is blocked or cancelled and cleared on leaving that state; `completed_at` is set exactly while completed. `start`, `block`, `unblock` and `complete` need `tasks.manage` or `tasks.work` on a task assigned to the caller or one of the caller's Teams; `cancel`, `reopen`, editing and assignment need `tasks.manage`. The reasons given for `block`, `cancel` and `reopen` are written to the audit log (they are free text, up to 500 characters, and audit records cannot be redacted; titles and descriptions are never audited). Every change increments `version` (`expectedVersion` is required to edit a task and optional on the other operations; it guards against lost updates), is audited and emits `TaskAssigned` (assignment), `TaskCompleted` (complete) and `TaskCancelled` (cancel, including cancellation of all tasks of a context record by its owning module).

## Virtual Machine
`running | stopped | unknown ⇄ each other`, then `decommissioned` (terminal tombstone). Implemented (F7a, `modules/infrastructure`): `ChangeVMState` moves between `running`, `stopped` and `unknown` (the state is hand-entered, not observed); `DecommissionVM` needs a reason code (`retired|migrated|deleted|other`) and from then on no operation is accepted. A decommissioned VM's name can be reused.

## Service
`operational ⇄ degraded ⇄ outage ⇄ planned` (any of these four to any other, with a reason code `incident|maintenance|recovered|rollout|correction|other`), then `retired` (terminal tombstone). Implemented (F7b, `modules/services`): `ChangeStatus(reason)` moves between the four live statuses (a request for the current status is a no-op); `Retire(reason)` (`replaced|decommissioned|merged|error_correction|other`) ends the Service's own dependency links and from then on no operation is accepted; a retried retire with the same reason returns the current record. The status is hand-entered, not observed. The name of a retired Service can be reused. `expectedVersion` is required.

## Rack Placement
`active → removed`. A move closes the active placement with the internal reason `moved` and opens a new active placement linked through `previous_placement_id`; `RemoveAsset` closes it with `relocated|replaced|decommissioned|error_correction|other`. Closed placements are history and never reopened; place the Asset again instead. Buildings, Rooms and Racks are `active ⇄ archived` and cannot be archived while the level below is active or holds active placements.

## Change
`draft → assessment → pending_approval? → approved → scheduled → in_progress → completed → review? → closed`.

Terminal/exception branches: `rejected`, `failed`, `cancelled`. Emergency change can use an abbreviated explicit policy path; it does not bypass audit.

Implemented (F7c, `modules/changes`; the status is only ever changed by these explicit operations, each audited with ids and reason codes, recorded in the append-only `change_transitions` (no update, delete or truncate; a Change with history cannot be deleted), and guarded by a required `expectedVersion`):

| Operation | From | To | Notes |
|---|---|---|---|
| `Create` | - | `draft` | requester = caller; kind `standard\|normal\|emergency`; risk defaults to `low` |
| `UpdateDetails`, `AddAffected`, `RemoveAffected` | `draft`, `assessment` | same | editors are remembered and can never assess or approve; the kind can only change in `draft`; removing a resource of a type the caller may not see is `changes.invalid_reference` |
| `Submit` | `draft` | `assessment` | needs a window, a rollback plan for `medium`/`high` risk and, unless `standard`, at least one affected resource |
| `Assess(risk, approver \| emergencyJustification)` | `assessment` | `pending_approval`, or `approved` | repeats Submit's checks for the assessed risk; refused for the requester and editors (`changes.separation_of_duties`); `medium`/`high` risk and every `emergency` change need an approver (user or team; requester, owner, editors and assessor excluded) - then `pending_approval`; `low` risk needs none - `approved`; an emergency change may instead carry an explicit justification (`emergency_approved`, reason `emergency`, the assessor recorded as `emergencyApprovedBy`) |
| approval decided (`ApprovalDecided` consumer) | `pending_approval` | `approved` or `rejected` | the event must name the pending approval (else a permanent error); approval records the approved window; `rejected` is terminal (reason `approval_rejected`) |
| `Schedule([window])` | `approved` | `scheduled` | the window must start in the future and, after an approver's approval, lie within the approved window (`changes.window_not_approved`; a different window needs a new Change); an `emergency` change may use any window starting at most one hour in the past |
| `Start` | `scheduled` | `in_progress` | owner or `changes.execute` |
| `Complete([force=tasks_waived])` | `in_progress` | `completed` | open execution Tasks block it; `tasks_waived` cancels them and is recorded |
| `Fail(reason, rollbackDone)` | `in_progress` | `failed` | reason `execution_error\|verification_failed\|window_exceeded\|dependency_unavailable\|other` |
| `Review(outcomeNote)` | `completed`, `failed` | `review` | the outcome note is required; optional step, except for emergency changes; not by the emergency approver |
| `Close` | `completed`, `failed`, `review` | `closed` | an `emergency` change cannot be closed without a review (`changes.review_required`) nor by its emergency approver; open execution Tasks are cancelled (reason `change_closed`, counted in the audit entry) |
| `Cancel(reason)` | `draft`..`scheduled` | `cancelled` | cancels a pending approval and the Tasks of a scheduled change; reason `no_longer_needed\|superseded\|rescheduled\|risk_too_high\|error_correction\|other` |

Reopening is not supported: `rejected`, `failed` (until closed), `cancelled` and `closed` changes are never edited; create a new Change instead. Execution Tasks (`AddTask`) can be added while `scheduled` or `in_progress`.

The `change AFFECTS ...` Relationships are current while the Change is open and end in the same transaction that makes it `closed`, `cancelled` or `rejected` (end reasons `change_closed`, `change_cancelled`, `change_rejected`); the Change's detail and the affected-resource list filter still read them by that reason.

Database invariants (CHECK constraints of `changes.changes`): `started_at` is set exactly in `in_progress`, `completed`, `failed`, `review`, `closed`; `completed_at` exactly in `completed`, `failed`, `review`, `closed`; `closed_at` exactly in `closed`, `cancelled`, `rejected`; `started_at <= completed_at <= closed_at`; `failed` has `rollback_done` and a reason; `pending_approval` has an `approval_id`; `review` (and a closed emergency change) has an outcome note; an emergency justification exists only on `emergency` changes and always with its approver.

## Initiative
`idea → planning → proposed → approved → active ↔ on_hold → completed`, with `cancelled` alternatives and `approved|active|on_hold → planning` through `Replan`.

Implemented (F7d, `modules/planning`; the status is only ever changed by these explicit operations, each requiring `expectedVersion` (except the approval consumer), audited as `planning.initiative.<operation>` with ids, states and reason codes, recorded in the append-only `initiative_transitions` and published as `InitiativeStatusChanged`):

| Operation | From | To | Notes |
|---|---|---|---|
| `Create` | - | `idea` | owner defaults to the caller; must be an active User |
| `UpdateDetails`, `AddItem`, `RemoveItem`, Milestone add/update/remove | `idea`, `planning` | same | material changes require a fresh approval after `Replan`; editors are remembered and can never approve |
| Milestone complete/reopen | `idea`, `planning`, `approved`, `active`, `on_hold` | same | operational progress remains editable; the actor is recorded as an editor without bumping the Initiative version |
| `StartPlanning` | `idea` | `planning` | |
| `Propose(approver user \| team)` | `planning` | `proposed` | requests an Approval (subject `initiative`); the owner, creator, proposer and editors are excluded (`planning.no_eligible_approver`) |
| approval approved (consumer `planning.approval`) | `proposed` | `approved` | sets `approved_at`; idempotent, stale events change nothing, a foreign approval id is a permanent error |
| approval rejected (consumer) | `proposed` | `planning` | reason `approval_rejected`; it can be changed and proposed again |
| `Replan(reason)` | `approved`, `active`, `on_hold` | `planning` | reasons `scope_change|priority_change|resource_change|error_correction|other`; clears approval, proposal and approval/activation timestamps; requires a new `Propose` and Approval |
| `Activate` | `approved` | `active` | sets `activated_at` |
| `Hold(reason)` | `active` | `on_hold` | reasons `blocked_dependency\|resource_shortage\|budget\|reprioritized\|other` |
| `Resume` | `on_hold` | `active` | |
| `Complete` | `active` | `completed` | active only, including after a hold has been resumed; sets `closed_at`; included records keep their own lifecycles |
| `Cancel(reason)` | any but `completed`, `cancelled` | `cancelled` | reasons `no_longer_needed\|superseded\|budget\|reprioritized\|error_correction\|other`; cancels a pending approval |

Database CHECKs: `proposed` has an approval and a proposer; approved, active, on hold and completed have an approval; `approved_at` is set exactly from `approved` on (free while `cancelled`); `activated_at` exactly in `active`, `on_hold`, `completed`; `closed_at` exactly in `completed`, `cancelled`; `on_hold` and `cancelled` carry a reason; timestamps in order. Transition statuses are constrained to the Initiative status set. Milestones: `open ↔ done` (`CompleteMilestone`, `ReopenMilestone`), `RemoveMilestone(reason)` (`no_longer_needed|merged|error_correction|other`) keeps the row. Milestone position is a sort hint, not a unique sequence.

## Deployment
Overall: `draft → scheduled? → resolving_targets → ready → running → completed | completed_with_errors | failed | cancelled`.

A few failed devices normally produce `completed_with_errors`, not overall `failed`.

DeploymentTarget: `pending → queued → running → successful | failed | expired`, plus `not_applicable/cancelled`. Retries create immutable DeploymentAttempt history.

Planned for provider-executed Deployments ([ADR-0027](../decisions/ADR-0027-software-management-providers.md)), replacing `queued/running` for those Deployments: DeploymentTarget `pending → assignment_requested → awaiting_observation → successful | failed | expired`, plus `already_satisfied` (the version was present before rollout), `not_applicable` and `cancelled`. `successful` requires a Management Observation newer than the ring's assignment read-back; stale or unknown evidence never counts as success, including for promotion thresholds. Deployment additionally gets `paused` (`running ↔ paused`).

Deployment Ring (planned): `pending → active → awaiting_promotion → promoted`, or `halted` (from `active`/`awaiting_promotion`, by failure threshold or decision; a halted ring can be resumed to `active`). Promotion requires the configured gate (Approval, fresh-evidence success threshold, soak time, Maintenance Window); the next ring becomes `active` only when the previous one is `promoted`.

## Software Approval Status (planned)
Software Product: `candidate → approved → deprecated → retired`; `blocked` can be entered from any state by decision with a reason and left only to `candidate` (re-evaluation). A version approval binds a Software Version to its installer SHA-256 (and publisher signature where available): `pending → approved | rejected`, `approved → revoked`; a changed hash, installer URL, install command or detection rule needs a new version approval. `approved` may be backed by an Approval record but is not one. Vendor end-of-life is a separate observed fact.

## Remote Access Session (planned)
`requested → pending_approval? → authorized → launched → closed`, or `rejected` (by policy or approver), `cancelled`, `expired` (launch handle not used in time), `failed` (launch failed). Consent is a separate field (`granted | declined | not_required | unknown`); provider-observed connect/disconnect are separate fields with source and observed time. Turaco never infers that a session ended without provider data or an explicit close. [ADR-0026](../decisions/ADR-0026-remote-access-providers.md).

## AI Proposal (planned)
`proposed → confirmed → executed | failed`, or `dismissed | expired`. Short-lived; confirmation is single-use, bound to the exact parameters and target record version; permissions are re-checked before execution. Not an Approval. [ADR-0029](../decisions/ADR-0029-turaco-ai.md).

## Security Advisory / Finding
F8a backend (`modules/security`, migration `000047_security.up.sql`; [design](../product/f8-security-briefing-design.md)): an Advisory starts `new` on `Create` or `Import`. `StartAnalysis` moves `new → analyzing`; `MarkApplicable` moves `new|analyzing → applicable`; `MarkNotApplicable(reason)` moves `new|analyzing|applicable → not_applicable`. From `applicable`, `PlanRemediation` moves to `remediation_planned`, `StartRemediation` moves directly to `remediating`, and `Resolve` may move directly to `resolved`; `StartRemediation` also accepts `remediation_planned`, and `Resolve` accepts `remediation_planned|remediating`. `Archive` moves `new|not_applicable|resolved → archived` (terminal). An imported metadata change returns an `applicable` Advisory to `analyzing` with `feed_changed`; a criteria change returns any live non-editable status, including `remediating`, `not_applicable` and `resolved`, to `analyzing` with `criteria_changed`. Editing or normalizing criteria on an applicable Advisory also returns it to `analyzing` with `criteria_changed`, is versioned and requeues matching. Archived Advisories stay archived and imports do not revive them. `MarkNotApplicable` accepts `product_not_used|version_not_used|configuration_not_affected|duplicate|other`; `other` additionally requires `security.accept_risk`. Every edit or lifecycle operation requires `expectedVersion`; state transitions are append-only and audited. Detail and summary expose `unmatchedCriteria`, and `MarkNotApplicable`, `Resolve` and `Archive` responses warn with `unmatched_criteria` when the count is nonzero.

A Finding starts `open` when an Advisory's criteria match an observed installation. `Investigate` moves `open → investigating`; `Accept` acknowledges an `open|investigating` Finding as `accepted`; `PlanRemediation` moves `open|investigating|accepted → remediation_planned`; `StartRemediation` moves `open|investigating|accepted|remediation_planned → remediating`. `MarkFalsePositive(reason)` and `AcceptRisk(reason, reviewBy)` resolve triage separately; risk acceptance needs `security.accept_risk`, the accepting User, and a review date after today and within twelve months. False-positive reasons: `version_misreported|product_mismatch|not_installed|configuration_not_affected|other`; `configuration_not_affected` and `other` additionally require `security.accept_risk`. Risk reasons: `compensating_control|low_exposure|no_fix_available|business_need|other`. `Reopen(reason)` returns `false_positive|risk_accepted → open` (`review_due|new_information|error_correction|other`). All human operations require `expectedVersion`, audit and an append-only transition. `security.match` uses audited system transitions: a newer nonmatching installation remediates with `version_changed` only when its raw version differs from the Finding's installed version; removal or Device tombstone also remediates. A criteria revision newer than the Finding's `lastSeenAt` makes an evaluation miss ambiguous and leaves the Finding stale and unchanged, including accepted risk. A newer changed version or `potential → probable` escalation reopens `false_positive|risk_accepted → open` with `version_changed`; renewed exposure reopens `remediated → open`. When `riskReviewBy` is before today, `security.match_all` idempotently reopens `risk_accepted → open` with `review_due`. Task completion never implies remediation.

## Endpoint Finding (data quality)
`open → resolved`. Raised by device ingestion (`no_asset_match`, `serial_conflict`, `duplicate_device`, `unmatched_software`), resolved automatically when a later ingestion no longer finds the condition; one open finding per kind and device. `provider_reported_error` is the provider's own failed/conflict statement, raised and resolved by the management ingestion; `assignment_ineffective` is Turaco's inference (assigned and expected applicable, but the provider shows nothing or `not_applicable` for more than 7 days), raised and resolved by a reconcile step after each management run (rotating cursor over the live Devices; a Device not reached in a run keeps its last state until the next pass). Both management kinds are readable only with management access. Security findings are a separate concept.

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
